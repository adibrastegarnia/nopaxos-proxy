// Copyright 2019-present Open Networking Foundation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package nopaxos

import (
	"container/list"
	"context"
	"github.com/atomix/atomix-go-node/pkg/atomix/cluster"
	"github.com/atomix/atomix-go-node/pkg/atomix/node"
	"github.com/atomix/atomix-nopaxos-node/pkg/atomix/nopaxos/protocol"
	"github.com/atomix/atomix-nopaxos-node/pkg/atomix/nopaxos/util"
	"math"
	"sync"
	"time"
)

const queueSize = 1000

// NewClient returns a new NOPaxos client
func NewClient(config cluster.Cluster, sequencerConfig *SequencerConfig) (*Client, error) {
	return newClient(NewCluster(config), sequencerConfig)
}

// newClient returns a new NOPaxos client
func newClient(cluster *Cluster, config *SequencerConfig) (*Client, error) {
	conns := list.New()
	for _, member := range cluster.Members() {
		stream, err := cluster.GetStream(member)
		if err != nil {
			return nil, err
		}
		conns.PushBack(stream)
	}
	quorum := int(math.Floor(float64(conns.Len())/2.0)) + 1
	client := &Client{
		logger:     util.NewNodeLogger("sequencer"),
		cluster:    cluster,
		config:     config,
		conns:      conns,
		writeChans: list.New(),
		readChans:  list.New(),
		quorum:     quorum,
		commands:   make(map[protocol.MessageID]*commandHandler),
		queries:    make(map[protocol.MessageID]*queryHandler),
		writes:     make(chan *requestContext, queueSize),
		reads:      make(chan *requestContext, queueSize),
		log:        util.NewNodeLogger(string(cluster.Member())),
	}
	client.connect()
	return client, nil
}

// Client is a service Client implementation for the NOPaxos consensus protocol
type Client struct {
	node.Client
	logger     util.Logger
	cluster    *Cluster
	config     *SequencerConfig
	conns      *list.List
	writeChans *list.List
	readChans  *list.List
	quorum     int
	commandID  protocol.MessageID
	queryID    protocol.MessageID
	commands   map[protocol.MessageID]*commandHandler
	queries    map[protocol.MessageID]*queryHandler
	writes     chan *requestContext
	reads      chan *requestContext
	mu         sync.RWMutex
	log        util.Logger
}

func (c *Client) connect() {
	conn := c.conns.Front()
	for conn != nil {
		stream := conn.Value.(protocol.ClientService_ClientStreamClient)
		go c.receive(stream)
		writeCh := make(chan *protocol.CommandRequest)
		c.writeChans.PushBack(writeCh)
		go c.sendWrites(stream, writeCh)
		readCh := make(chan *protocol.QueryRequest)
		c.readChans.PushBack(readCh)
		go c.sendReads(stream, readCh)
		conn = conn.Next()
	}
	go c.processWrites()
	go c.processReads()
}

func (c *Client) receive(stream protocol.ClientService_ClientStreamClient) {
	for {
		response, err := stream.Recv()
		if err != nil {
			return
		}

		switch r := response.Message.(type) {
		case *protocol.ClientMessage_CommandReply:
			if r.CommandReply.ViewID.SessionNum != c.config.SessionId {
				continue
			}
			c.mu.Lock()
			handler := c.commands[r.CommandReply.MessageNum]
			if handler != nil && handler.succeed(r.CommandReply.Value) {
				delete(c.commands, r.CommandReply.MessageNum)
				handler.complete()
			}
			c.mu.Unlock()
		case *protocol.ClientMessage_QueryReply:
			if r.QueryReply.ViewID.SessionNum != c.config.SessionId {
				continue
			}
		}
	}
}

// Write sends a write operation to the cluster
func (c *Client) Write(ctx context.Context, in []byte, ch chan<- node.Output) error {
	c.writes <- &requestContext{
		ctx:   ctx,
		value: in,
		ch:    ch,
	}
	return nil
}

// Read sends a read operation to the cluster
func (c *Client) Read(ctx context.Context, in []byte, ch chan<- node.Output) error {
	c.reads <- &requestContext{
		ctx:   ctx,
		value: in,
		ch:    ch,
	}
	return nil
}

func (c *Client) processWrites() {
	for context := range c.writes {
		handler := &commandHandler{ch: context.ch, quorum: c.quorum}
		messageID := c.commandID + 1
		c.commandID = messageID
		c.mu.Lock()
		c.commands[messageID] = handler
		c.mu.Unlock()

		c.enqueueWrite(&protocol.CommandRequest{
			SessionNum: c.config.SessionId,
			MessageNum: messageID,
			Timestamp:  time.Now(),
			Value:      context.value,
		})
	}
}

func (c *Client) enqueueWrite(request *protocol.CommandRequest) {
	c.logger.Request("CommandRequest", request)
	element := c.writeChans.Front()
	for element != nil {
		ch := element.Value.(chan *protocol.CommandRequest)
		ch <- request
		element = element.Next()
	}
}

func (c *Client) sendWrites(stream protocol.ClientService_ClientStreamClient, ch chan *protocol.CommandRequest) {
	for request := range ch {
		_ = stream.Send(&protocol.ClientMessage{
			Message: &protocol.ClientMessage_Command{
				Command: request,
			},
		})
	}
}

func (c *Client) processReads() {
	for context := range c.reads {
		handler := &queryHandler{ch: context.ch}
		messageID := c.queryID + 1
		c.queryID = messageID
		c.mu.Lock()
		c.queries[messageID] = handler
		c.mu.Unlock()

		c.enqueueRead(&protocol.QueryRequest{
			SessionNum: c.config.SessionId,
			MessageNum: messageID,
			Timestamp:  time.Now(),
			Value:      context.value,
		})
	}
}

func (c *Client) enqueueRead(request *protocol.QueryRequest) {
	c.logger.Request("QueryRequest", request)
	element := c.readChans.Front()
	for element != nil {
		ch := element.Value.(chan *protocol.QueryRequest)
		ch <- request
		element = element.Next()
	}
}

func (c *Client) sendReads(stream protocol.ClientService_ClientStreamClient, ch chan *protocol.QueryRequest) {
	for request := range ch {
		_ = stream.Send(&protocol.ClientMessage{
			Message: &protocol.ClientMessage_Query{
				Query: request,
			},
		})
	}
}

type requestContext struct {
	ctx   context.Context
	value []byte
	ch    chan<- node.Output
}

type commandContext struct {
	ctx     context.Context
	request *protocol.CommandRequest
	handler *commandHandler
}

type queryContext struct {
	ctx     context.Context
	request *protocol.QueryRequest
	handler *queryHandler
}

// commandHandler is a quorum reply handler
type commandHandler struct {
	quorum    int
	succeeded int
	failed    int
	value     []byte
	ch        chan<- node.Output
}

func (r *commandHandler) succeed(value []byte) bool {
	if r.value == nil && len(value) > 0 {
		r.value = value
	}
	r.succeeded++
	return r.value != nil && r.succeeded >= r.quorum
}

func (r *commandHandler) fail() bool {
	r.failed++
	return r.failed >= r.quorum
}

func (r *commandHandler) complete() {
	r.ch <- node.Output{
		Value: r.value,
	}
}

// queryHandler is a query reply handler
type queryHandler struct {
	ch chan<- node.Output
}

func (r *queryHandler) succeed(value []byte) bool {
	if len(value) > 0 {
		r.ch <- node.Output{
			Value: value,
		}
		return true
	}
	return false
}

// Close closes the client
func (c *Client) Close() error {
	return nil
}
