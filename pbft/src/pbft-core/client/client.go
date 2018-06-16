/*
Copyright (c) 2018 TrueChain Foundation

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"path"
	"strconv"
	"time"

	"pbft-core"
	"pbft-core/pbft-server"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	pb "pbft-core/fastchain"
)

var (
	cfg    = pbft.Config{}
	svList []*pbftserver.PbftServer
	cl     = Client{}
)

// Client makes queries to what it believes to be the primary replica.
// Below defines the major properties of a client resource
type Client struct {
	IP      string
	Port    int
	Index   int
	Me      int
	Cfg     *pbft.Config
	privKey *ecdsa.PrivateKey
}

// Start is a notifier of client's init state
func (cl *Client) Start() {
	pbft.MyPrint(1, "Firing up client executioner...\n")

}

// LoadPbftSimConfig loads configuration for running PBFT simulation
func LoadPbftSimConfig() {
	cfg.HostsFile = path.Join(os.Getenv("HOME"), "hosts") // TODO: read from config.yaml in future.
	cfg.IPList, cfg.Ports, cfg.GrpcPorts = pbft.GetIPConfigs(cfg.HostsFile)
	cfg.NumKeys = len(cfg.IPList)
	cfg.N = cfg.NumKeys - 1 // we assume client count to be 1
	cfg.NumQuest = 100
	cfg.GenerateKeysToFile(cfg.NumKeys)
}

// LoadPbftClientConfig loads client configuration
func (cl *Client) LoadPbftClientConfig() {
	cl.IP = cfg.IPList[cfg.N]
	cl.Port = cfg.Ports[cfg.N]
	cl.Me = 0
	cl.Cfg = &cfg

	pemkeyFile := fmt.Sprintf("sign%v.pem", cfg.N)
	sk := pbft.FetchPrivateKey(path.Join(cfg.KD, pemkeyFile))
	fmt.Println("just fetched private key for Client")
	fmt.Println(sk)
	cl.privKey = sk
}

func addSig(req *pb.Request, privKey *ecdsa.PrivateKey) {
	//MyPrint(1, "adding signature.\n")
	gob.Register(&pb.Request_Inner{})
	b := bytes.Buffer{}
	e := gob.NewEncoder(&b)
	err := e.Encode(req.Inner)
	if err != nil {
		pbft.MyPrint(3, "%s err:%s", `failed to encode!\n`, err.Error())
		return
	}

	s := pbft.GetHash(string(b.Bytes()))
	pbft.MyPrint(1, "digest %s.\n", string(s))
	req.Dig = []byte(pbft.DigType(s))
	if privKey != nil {
		sigr, sigs, err := ecdsa.Sign(rand.Reader, privKey, []byte(s))
		if err != nil {
			pbft.MyPrint(3, "%s", "Error signing.")
			return
		}

		req.Sig = &pb.Request_MsgSignature{R: sigr.Int64(), S: sigs.Int64()}
	}
}

// NewRequest takes in a message and timestamp as params for a new request from client
func (cl *Client) NewRequest(msg string, timeStamp int64) {
	//broadcast the request
	for i := 0; i < cfg.N; i++ {
		req := &pb.Request{
			Inner: &pb.Request_Inner{
				Id:        int32(cfg.N),
				Seq:       0,
				View:      0,
				Type:      int32(pbft.TypeRequest),
				Msg:       []byte(pbft.MsgType(msg)),
				Timestamp: timeStamp,
			},
		}

		addSig(req, cl.privKey)
		conn, err := grpc.Dial(fmt.Sprintf("localhost:%d", cfg.GrpcPorts[i]), grpc.WithInsecure())
		if err != nil {
			log.Fatalf("did not connect: %v", err)
		}

		c := pb.NewFastChainClient(conn)
		ctx := context.TODO()

		checkLeaderResp, err := c.CheckLeader(ctx, &pb.CheckLeaderReq{})
		if err != nil {
			log.Fatalf("could not check if node is leader: %v", err)
		}
		fmt.Printf("%d", checkLeaderResp.Message)

		if !checkLeaderResp.Message {
			continue
		}

		resp, err := c.NewTxnRequest(ctx, req)
		if err != nil {
			log.Fatalf("could not send transaction request to pbft node: %v", err)
		}

		fmt.Printf("%s\n", resp.Msg)
		conn.Close()
		break
	}
}

// StartPbftServers starts PBFT servers from config information
func StartPbftServers() {
	svList = make([]*pbftserver.PbftServer, cfg.N)
	for i := 0; i < cfg.N; i++ {
		fmt.Println(cfg.IPList[i], cfg.Ports[i], i)
		svList[i] = pbftserver.BuildServer(cfg, cfg.IPList[i], cfg.Ports[i], cfg.GrpcPorts[i], i)
	}

	for i := 0; i < cfg.N; i++ {
		<-svList[i].Nd.ListenReady
	}

	time.Sleep(1 * time.Second) // wait for the servers to accept incoming connections
	for i := 0; i < cfg.N; i++ {
		svList[i].Nd.SetupReady <- true // make them to dial each other's RPCs
	}

	//fmt.Println("[!!!] Please allow the program to accept incoming connections if you are using Mac OS.")
	time.Sleep(1 * time.Second) // wait for the servers to accept incoming connections
}

func main() {
	LoadPbftSimConfig()
	StartPbftServers()
	cl := &Client{}
	cl.LoadPbftClientConfig()

	go cl.Start() // in case client has some initial logic

	start := time.Now()
	for k := 0; k < cfg.NumQuest; k++ {
		cl.NewRequest("Request "+strconv.Itoa(k), time.Now().Unix()) // A random string Request{1,2,3,4....}
	}

	fmt.Println("Finish sending the requests.")
	elapsed := time.Since(start)

	finish := make(chan bool)
	for i := 0; i < cfg.N; i++ {
		go func(ind int) {
			for {
				// place where channel data is extracted out of Node's channel context
				c := <-svList[ind].Out
				if c.Index == cfg.NumQuest {
					finish <- true
				}
			}

		}(i)
	}
	<-finish
	fmt.Println("Test finished. Time cost:", elapsed)
}
