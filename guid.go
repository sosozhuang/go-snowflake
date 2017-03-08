package main

import (
	"fmt"
	"time"
	"log"
	"sync"
	"golang.org/x/net/context"
	//pb "github.com/sosozhuang/guid/proto"
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

type idWorker struct {
	workerId      uint64
	datacenterId  uint64
	sequence      uint64
	lastTimestamp int64
	m             sync.Mutex
}

func (iw *idWorker) NextId() (uint64, error) {
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

func timeUnixMillis() int64 {
	return time.Now().UnixNano() / 1e6
}

func NewIdWorker(workerId, datacenterId, sequence uint64) (*idWorker, error) {
	if workerId > maxWorkerId || workerId < 0 {
		return nil, fmt.Errorf("worker Id can't be greater than %d or less than 0", maxWorkerId)
	}
	if datacenterId > maxDatacenterId || datacenterId < 0 {
		return nil, fmt.Errorf("datacenter Id can't be greater than %d or less than 0", maxDatacenterId)
	}

	log.Printf("Worker starting. timestamp left shift %d, datacenter id bits %d, worker id bits %d, sequence bits %d, workerid %d\n",
		timestampLeftShift, datacenterIdBits, workerIdBits, sequenceBits, workerId)
	iw := &idWorker{
		workerId:      workerId,
		datacenterId:  datacenterId,
		sequence:      sequence,
		lastTimestamp: -1,
	}
	return iw, nil
}



func (iw *idWorker) GetIdWorker(context.Context, *EmptyRequest) (*IdWorkerReply, error) {
	return &IdWorkerReply{
		WorkerId:     iw.workerId,
		DatacenterId: iw.datacenterId,
		Timestamp:    timeUnixMillis(),
	}, nil
}

func (iw *idWorker) GetId(context.Context, *EmptyRequest) (*IdReply, error) {
	id, err := iw.NextId()
	return &IdReply{id}, err
}
