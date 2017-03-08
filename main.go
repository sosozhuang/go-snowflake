package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/coreos/etcd/client"
	"github.com/sosozhuang/guid/guid"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	serverHost       = flag.String("host", getHostname(), "server host")
	serverPort       = flag.Int("port", 7609, "server port")
	workerId         = flag.Uint64("worker-id", 0, "worker id")
	datacenterId     = flag.Uint64("datacenter-id", 0, "data center id")
	sequence         = flag.Uint64("seq", 0, "sequence")
	etcdEndpoints    = flag.String("etcd", "http://127.0.0.1:2379", "etcd emdpoints")
	workerIdPath     = flag.String("path", "/snowflake-servers", "worker id path")
	skipSanityChecks = flag.Bool("skip-check", false, "skip sanity checks")
	//startupSleepMs   = flag.Int64("sleep", 10000, "startup sleep milliseconds")
)

func main() {
	flag.Parse()

	c, err := etcdClient(*etcdEndpoints)
	if err != nil {
		log.Fatalln("Failed to create etcd client:", err)
		return
	}
	if !*skipSanityChecks {
		err := sanityCheckPeers(c)
		if err != nil {
			log.Fatalln("Unexpected exception while checking peers:", err)
			return
		}
	}

	err = registerWorkerId(*workerId, c)
	if err != nil {
		log.Fatalln("Unexpected exception while registering worker id:", err)
		return
	}
	defer unRegisterWorkerId(*workerId, c)
	go signalListen(*workerId, c)

	//time.Sleep(time.Duration(*startupSleepMs) * time.Millisecond)
	iw, err := guid.NewIdWorker(*workerId, *datacenterId, *sequence)
	if err != nil {
		log.Println("Unexpected exception while initializing server:", err)
		return
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *serverPort))
	if err != nil {
		log.Printf("Failed to listen: %v", err)
		return
	}
	s := grpc.NewServer()
	guid.RegisterWorkerServer(s, iw)
	// Register reflection service on gRPC server.
	reflection.Register(s)
	if err := s.Serve(lis); err != nil {
		log.Printf("Failed to serve: %v", err)
		return
	}

}

type Peer struct {
	Hostname string
	Port     int
}

func getHostname() string {
	hostname, _ := os.Hostname()
	return hostname
}

func sanityCheckPeers(c client.Client) error {
	var timestampCount, peerCount int64 = 0, 0
	peerMap, err := peers(c)
	if err != nil {
		return err
	}
	for key, value := range peerMap {
		id, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			log.Println("Parse peerMap key faild:", err)
			break
		}

		slice := strings.Split(value, ":")
		if len(slice) != 2 {
			log.Printf("PeerMap key %s value %s length %d\n", key, value, len(slice))
			break
		}
		port, err := strconv.Atoi(slice[1])
		if err != nil {
			log.Println("Parse peerMap value faild:", err)
			break
		}
		peer := Peer{slice[0], port}

		if peer.Hostname != *serverHost || peer.Port != *serverPort {
			log.Printf("Connecting to %s:%d\n", peer.Hostname, peer.Port)
			conn, err := grpc.Dial(fmt.Sprintf("%s:%d", peer.Hostname, peer.Port), grpc.WithInsecure())
			if err != nil {
				log.Printf("Did not connect: %v", err)
				return err
			}
			c := guid.NewWorkerClient(conn)
			worker, err := c.GetIdWorker(context.Background(), &guid.Request{})
			if err != nil {
				log.Fatalf("Could not get worker: %v", err)
				return err
			}
			conn.Close()
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
			timestampCount += worker.GetTimestamp()
		}
	}
	if peerCount > 0 {
		avg := timestampCount / peerCount
		now := timeUnixMillis()
		if math.Abs(float64(now-avg)) > 1e4 {
			log.Printf("Timestamp sanity check failed. Mean timestamp is %d, but mine is %d, "+
				"so I'm more than 10s away from the mean\n", avg, now)
			return errors.New("timestamp sanity check failed")

		}
	}
	return nil
}

func timeUnixMillis() int64 {
	return time.Now().UnixNano() / 1e6
}

func etcdClient(etcdEndpoints string) (client.Client, error) {
	cfg := client.Config{
		Endpoints: strings.Split(etcdEndpoints, ","),
		Transport: client.DefaultTransport,
	}
	return client.New(cfg)
}

func peers(c client.Client) (map[string]string, error) {
	kapi := client.NewKeysAPI(c)
	peerMap := make(map[string]string)
	resp, err := kapi.Get(context.Background(), *workerIdPath, &client.GetOptions{Recursive: true})
	if err != nil {
		e, ok := err.(client.Error)
		if ok {
			if e.Code == client.ErrorCodeKeyNotFound {
				log.Printf("Key %s missing, trying to create it\n", *workerIdPath)
				_, err = kapi.Set(context.Background(), *workerIdPath, "", &client.SetOptions{Dir: true})
				return peerMap, err
			}
		}
		return nil, err
	}

	for _, child := range resp.Node.Nodes {
		_, key := path.Split(child.Key)
		peerMap[key] = child.Value
	}

	log.Printf("Found %d children\n", len(resp.Node.Nodes))
	return peerMap, nil
}

func registerWorkerId(workerId uint64, c client.Client) error {
	log.Printf("Trying to claim workerId %d\n", workerId)
	tries := 0
	for {
		kapi := client.NewKeysAPI(c)
		_, err := kapi.Create(context.Background(), fmt.Sprintf("%s/%d", *workerIdPath, workerId), fmt.Sprintf("%s:%d", *serverHost, *serverPort))
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

func unRegisterWorkerId(workerId uint64, c client.Client) {
	log.Printf("trying to declaim workerId %d\n", workerId)
	tries := 0
	for {
		kapi := client.NewKeysAPI(c)
		_, err := kapi.Delete(context.Background(), fmt.Sprintf("%s/%d", *workerIdPath, workerId), nil)
		if err != nil {
			if tries < 2 {
				log.Printf("Failed to declaim worker id. Gonna wait a bit and retry because the node may be from the last time I was running.")
				tries += 1
				time.Sleep(1000)
			} else {
				return
			}
		} else {
			break
		}

	}
	log.Printf("Successfully declaimed workerId %d", workerId)
}

func signalListen(workerId uint64, c client.Client) {
	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(ch)
	<-ch
	unRegisterWorkerId(workerId, c)
	os.Exit(0)
}
