# guid
guid: based on [snowflake](https://github.com/twitter/snowflake), guid is written in golang. Call local function `guid.NewIdWorker`, will get an id worker. In addition, making generate id as a network service, a client can connect with [grpc](https://github.com/grpc/grpc-go). Servers information are stored in [etcd](https://github.com/coreos/etcd)


## Usage of guid:
* Run as network service
```shell
  -datacenter-id uint
    	data center id
  -etcd string
    	etcd endpoints (default "http://127.0.0.1:2379")
  -host string
    	server host (default "localhost")
  -path string
    	worker id path (default "/snowflake-servers")
  -port int
    	server port (default 7609)
  -seq uint
    	sequence
  -skip-check
    	skip sanity checks
  -worker-id uint
    	worker id
```
* Connect to remote service
```go
	//connect to grpc server
	conn, err := grpc.Dial(fmt.Sprintf("%s:%d", "localhost", 7609), grpc.WithInsecure())
	if err != nil {
		log.Printf("Did not connect: %v", err)
		return
	}
	defer conn.Close()
	c := guid.NewWorkerClient(conn)
	//how many ids you want
	number := uint32(10)
	reply, err := c.GetId(context.Background(), &guid.Request{number})
	if err != nil {
		log.Fatalf("Could not get worker: %v", err)
		return
	}
	log.Printf("Fetch Ids %v\n", reply.Id)
```
* Local call
```go
	var workerId, datacenterId, sequence uint64 = 0, 1, 2
	idWorker, err := guid.NewIdWorker(workerId, datacenterId, sequence)
	if err != nil {
		log.Println("Failed to make id worker:", err)
		return
	}
	id, err := idWorker.NextId()
	if err != nil {
		log.Println("Failed to get next id:", err)
		return
	}
	fmt.Printf("Fetch Id %d\n", id)
```
