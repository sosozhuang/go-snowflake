package guid

import "testing"

func TestNewIdWorker(t *testing.T) {
	var workerId, datacenterId, sequence uint64 = 0, 1, 2
	_, err := NewIdWorker(workerId, datacenterId, sequence)
	if err != nil {
		t.Error("Create id worker error")
	}
	workerId, datacenterId, sequence = -1^(-1<<workerIdBits)+1, -1 ^ (-1 << workerIdBits) + 2, 0
	_, err = NewIdWorker(workerId, datacenterId, sequence)
	if err == nil {
		t.Error("Create id worker should error")
	}
}

func TestIdWorker_NextId(t *testing.T) {
	var workerId, datacenterId, sequence uint64 = 0, 1, 2
	idWorker, err := NewIdWorker(workerId, datacenterId, sequence)
	if err != nil {
		t.Error("Create id worker error")
	}
	_, err = idWorker.NextId()
	if err != nil {
		t.Error("Generate next id error")
	}

}

func BenchmarkIdWorker_NextId(b *testing.B) {
	var workerId, datacenterId, sequence uint64 = 0, 1, 2
	idWorker, err := NewIdWorker(workerId, datacenterId, sequence)
	if err != nil {
		b.Error("Create id worker error")
	}
	for i := 0; i < b.N; i++ {
		idWorker.NextId()
	}
}
