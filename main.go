package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/coreos/etcd/client"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"os"
)

const (
	workerIdBits     uint64 = 5
	datacenterIdBits uint64 = 5
	maxWorkerId             = -1 ^ (-1 << workerIdBits)
	maxDatacenterId         = -1 ^ (-1 << datacenterIdBits)
	sequenceBits     uint64 = 12

	workerIdShift            = sequenceBits
	datacenterIdShift        = sequenceBits + workerIdBits
	timestampLeftShift       = sequenceBits + workerIdBits + datacenterIdBits
	sequenceMask             = -1 ^ (-1 << sequenceBits)
	twepoch            int64 = 1288834974657
)

var (
	serverPort       = flag.Int64("port", 7609, "server port")
	workerId         = flag.Uint64("wid", 0, "worker id")
	datacenterId     = flag.Uint64("dcid", 0, "data center id")
	sequence         = flag.Uint64("sequence", 0, "sequence")
	workerIdPath     = flag.String("path", "/snowflake-servers", "worker id path")
	skipSanityChecks = flag.Bool("check", false, "skip sanity checks")
	startupSleepMs   = flag.Int64("sleep", 10000, "startup sleep milliseconds")
)

func main() {
	flag.Parse()

	if !*skipSanityChecks {
		err := sanityCheckPeers()
		if err != nil {
			log.Fatalln(err)
			return
		}
	}

	err := registerWorkerId(*workerId)
	if err != nil {
		log.Fatalln(err)
		return
	}

	time.Sleep(time.Duration(*startupSleepMs) * time.Millisecond)
	iw, err := NewIdWorker(*workerId, *datacenterId, *sequence)
	if err != nil {
		log.Fatalf("Unexpected exception while initializing server: %s\n", err)
		return
	}
	fmt.Println(iw.NextId())
	fmt.Println(iw.NextId())
	time.Sleep(2 * time.Second)
	fmt.Println(iw.NextId())
}

type Peer struct {
	Hostname string
	Port     int64
}

func getHostname() string {
	hostname, _ := os.Hostname()
	return hostname
}

func sanityCheckPeers() error {
	peerCount := 0
	timestamps := int64(0)
	peerMap, err := peers()
	if err != nil {
		return err
	}
	for key, value := range peerMap {
		id, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			log.Println("parse peerMap key faild:", err)
			break
		}

		slice := strings.Split(value, ":")
		if len(slice) != 2 {
			log.Printf("peerMap key %s value %s length %d\n", key, value, len(slice))
			break
		}
		port, err := strconv.ParseInt(slice[1], 10, 64)
		if err != nil {
			log.Println("parse peerMap value faild:", err)
			break
		}
		peer := Peer{slice[0], port}

		if peer.Hostname != getHostname() && peer.Port != *serverPort {
			log.Printf("connecting to %s:%d\n", peer.Hostname, peer.Port)
			worker := IdWorker{}
			reportedWorkerId := worker.GetWorkerId()
			if reportedWorkerId != id {
				log.Printf("Worker at %s:%d has id %d in zookeeper, but via rpc it says %d", peer.Hostname, peer.Port, id, reportedWorkerId)
				return errors.New("worker id insanity")
			}
			reportedDatacenterId := worker.GetDatacenterId()
			if reportedWorkerId != *datacenterId {
				log.Printf("Worker at %s:%d has datacenter_id %d, but ours is %d",
					peer.Hostname, peer.Port, reportedDatacenterId, datacenterId)
				return errors.New("datacenter id insanity")
			}
			peerCount += 1
			timestamps += worker.timeUnixMillis()
		}
	}
	if peerCount > 0 {
		avg := timestamps / peerCount
		now := timeUnixMillis()
		if math.Abs(now-avg) > 1e4 {
			log.Printf("Timestamp sanity check failed. Mean timestamp is %d, but mine is %d, "+
				"so I'm more than 10s away from the mean\n", avg, now)
			return errors.New("timestamp sanity check failed")

		}
	}
	return nil
}

func peers() (map[string]string, error) {
	cfg := client.Config{
		Endpoints: []string{""},
		Transport: client.DefaultTransport,
	}
	c, err := client.New(cfg)
	if err != nil {
		return nil, err
	}
	kapi := client.NewKeysAPI(c)
	peerMap := make(map[string]string)
	resp, err := kapi.Get(context.Background(), *workerIdPath, &client.GetOptions{Recursive: true})
	if err != nil {
		if err == client.ErrorCodeKeyNotFound {
			log.Printf("%s missing, trying to create it\n", *workerIdPath)
			kapi.Set(context.Background(), *workerIdPath, "", &client.SetOptions{Dir: true})
			err = nil
		}
		return peerMap, err
	}

	for _, child := range resp.Node.Nodes {
		peerMap[child.Key] = child.Value
	}

	log.Printf("found %d children\n", len(resp.Node.Nodes))
	return peerMap, nil
}

func registerWorkerId(workerId uint64) error {
	log.Printf("trying to claim workerId %d\n", workerId)
	tries := 0
	for {
		cfg := client.Config{
			Endpoints: []string{""},
			Transport: client.DefaultTransport,
		}
		c, err := client.New(cfg)
		if err != nil {
			return nil, err
		}
		kapi := client.NewKeysAPI(c)
		_, err = kapi.Create(context.Background(), fmt.Sprintf("%s/%d", *workerIdPath, workerId), fmt.Sprintf("%s:%d", getHostname(), *serverPort))
		if err != nil {
			if tries < 2 {
				log.Printf("Failed to claim worker id. Gonna wait a bit and retry because the node may be from the last time I was running.")
				tries += 1
				time.Sleep(1000)
			} else {
				return err
			}
		} else {
			break
		}

	}
	log.Printf("Successfully claimed workerId %d", workerId)
	return nil
}

type IdWorker struct {
	workerId      uint64
	datacenterId  uint64
	sequence      uint64
	lastTimestamp int64
	m             sync.Mutex
}

func (iw *IdWorker) GetWorkerId() uint64 {
	return iw.workerId
}

func (iw *IdWorker) GetDatacenterId() uint64 {
	return iw.datacenterId
}

func (iw *IdWorker) NextId() (uint64, error) {
	iw.m.Lock()
	defer iw.m.Unlock()
	timestamp := timeUnixMillis()
	if timestamp < iw.lastTimestamp {
		log.Printf("clock is moving backwards. Rejecting requests until %d.\n", iw.lastTimestamp)
		return 0, fmt.Errorf("Clock moved backwards. Refusing to generate id for %d milliseconds",
			iw.lastTimestamp-timestamp)
	}
	if iw.lastTimestamp == timestamp {
		iw.sequence = (iw.sequence + 1) & sequenceMask
		if iw.sequence == 0 {
			timestamp = tilNextMillis(iw.lastTimestamp)
		}
	} else {
		iw.sequence = 0
	}
	iw.lastTimestamp = timestamp
	return (uint64(timestamp-twepoch) << timestampLeftShift) |
		(iw.datacenterId << datacenterIdShift) |
		(iw.workerId << workerIdShift) |
		iw.sequence, nil
}

func tilNextMillis(lastTimestamp int64) int64 {
	timestamp := timeUnixMillis()
	for timestamp <= lastTimestamp {
		timestamp = timeUnixMillis()
	}
	return timestamp
}

func (*IdWorker) timeUnixMillis() int64 {
	return time.Now().UnixNano() / 1e6
}

func NewIdWorker(workerId, datacenterId, sequence uint64) (*IdWorker, error) {
	if workerId > maxWorkerId || workerId < 0 {
		return nil, fmt.Errorf("worker Id can't be greater than %d or less than 0", maxWorkerId)
	}
	if datacenterId > maxDatacenterId || datacenterId < 0 {
		return nil, fmt.Errorf("datacenter Id can't be greater than %d or less than 0", maxDatacenterId)
	}

	log.Printf("Worker starting. timestamp left shift %d, datacenter id bits %d, worker id bits %d, sequence bits %d, workerid %d\n",
		timestampLeftShift, datacenterIdBits, workerIdBits, sequenceBits, workerId)
	iw := &IdWorker{
		workerId:      workerId,
		datacenterId:  datacenterId,
		sequence:      sequence,
		lastTimestamp: -1,
	}
	return iw, nil
}
