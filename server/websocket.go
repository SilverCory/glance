package server

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

type socketServer struct {
	clients      []*websocket.Conn
	upgrader     websocket.Upgrader
	totalShards  int
	patronBots   int
	patronShards int
	shardCache   map[int]map[int]int
	updateList   chan ShardUpdate
}

func newSocketServer() *socketServer {
	totalShards, err := strconv.ParseInt(os.Getenv("BOT_SHARDS"), 10, 32)
	if err != nil {
		log.Panicln(err)
	}

	totalPatrons, err := strconv.ParseInt(os.Getenv("PATRON_BOTS"), 10, 32)
	if err != nil {
		log.Panicln(err)
	}

	patronShards, err := strconv.ParseInt(os.Getenv("PATRON_SHARDS"), 10, 32)
	if err != nil {
		log.Panicln(err)
	}

	cache := make(map[int]map[int]int, 1+totalPatrons)
	cache[0] = make(map[int]int, totalShards)

	total := int(totalPatrons)
	for i := 1; i < total+1; i++ {
		cache[i] = make(map[int]int, patronShards)
	}

	return &socketServer{
		clients:      make([]*websocket.Conn, 0),
		upgrader:     websocket.Upgrader{},
		totalShards:  int(totalShards),
		patronBots:   total,
		patronShards: int(patronShards),
		shardCache:   cache,
		updateList:   make(chan ShardUpdate, 128),
	}
}

func (s *socketServer) socketUpgrader(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		w.WriteHeader(500)
		return
	}
	s.sayHello(conn)
	s.clients = append(s.clients, conn)

	closeHandler := func(code int, reason string) error {
		log.Println(conn.RemoteAddr(), "closed", code, "with", reason)
		s.die(conn)
		return nil
	}
	conn.SetCloseHandler(closeHandler)
}

func (s *socketServer) run() {
	demo := os.Getenv("ENABLE_DEMO")
	if demo != "" {
		demo, err := strconv.ParseBool(demo)
		if err != nil {
			log.Panicln(err)
		}
		if demo {
			log.Println("DEMO MODE enabled!")
			go s.testData()
		}
	}

	t := time.NewTicker(time.Second * 5)
	defer t.Stop()

	for {
		select {
		case u := <-s.updateList:
			s.sayUpdate(u)
			s.shardCache[u.Bot][u.ID] = u.Status
			s.saveState()
		case <-t.C:
			s.sayTick()
		}
	}
}

func (s *socketServer) sayHello(conn *websocket.Conn) {
	data := Message{
		Op: OpHello,
		Data: HelloMessage{
			TotalShards:  s.totalShards,
			TotalPatrons: s.patronBots,
			PatronShards: s.patronShards,
			State:        s.shardCache,
		},
	}
	err := conn.WriteJSON(data)
	if err != nil {
		log.Println(err)
		s.die(conn)
	}
	log.Println(conn.RemoteAddr(), "opened and saluted")
}

func (s *socketServer) sayUpdate(u ShardUpdate) {
	data := Message{
		Op: OpUpdate,
		Data: UpdateMessage{
			Bot:    u.Bot,
			Shard:  u.ID,
			Status: u.Status,
		},
	}

	b, err := json.Marshal(data)
	if err != nil {
		log.Panicln(err)
	}

	pm, err := websocket.NewPreparedMessage(websocket.TextMessage, b)
	if err != nil {
		log.Panicln(err)
	}

	for _, c := range s.clients {
		err = c.WritePreparedMessage(pm)
		if err != nil {
			log.Println(err)
			s.die(c)
		}
	}
}

func (s *socketServer) sayTick() {
	data := Message{
		Op:   OpTick,
		Data: true,
	}

	b, err := json.Marshal(data)
	if err != nil {
		log.Panicln(err)
	}

	pm, err := websocket.NewPreparedMessage(websocket.TextMessage, b)
	if err != nil {
		log.Panicln(err)
	}

	for _, c := range s.clients {
		err = c.WritePreparedMessage(pm)
		if err != nil {
			log.Println(err)
			s.die(c)
		}
	}
}

func (s *socketServer) die(conn *websocket.Conn) {
	conn.Close()
	// find index
	i := -1
	for idx, c := range s.clients {
		if c == conn {
			i = idx
			break
		}
	}
	if i == -1 {
		log.Println("failed to find index of socket")
		return
	}

	// reassign s.clients for brevity
	c := s.clients
	// swap conn with the back of the array
	// [0 1 2 3 4] removing 1 turns into [0 4 2 3 1]
	c[len(c)-1], c[i] = c[i], c[len(c)-1]
	// set clients to the array - 1
	// [0 4 2 3]
	s.clients = c[:len(c)-1]
}

// demo mode
func (s *socketServer) testData() {
	src := rand.NewSource(time.Now().UnixNano())
	rng := rand.New(src)

	// populate initial list with data
	for i := 0; i < s.totalShards; i++ {
		// [1, 5) - don't want to send unknowns
		status := rng.Intn(4) + 1
		u := ShardUpdate{
			ID:     i,
			Status: status,
		}
		s.updateList <- u
	}

	for {
		shard := rng.Intn(s.totalShards)
		// [1, 5) - don't want to send unknowns
		status := rng.Intn(4) + 1

		u := ShardUpdate{
			ID:     shard,
			Status: status,
		}
		s.updateList <- u

		time.Sleep(time.Second * 1)
	}
}

func (s *socketServer) saveState() {
	b := new(bytes.Buffer)
	e := gob.NewEncoder(b)

	err := e.Encode(s.shardCache)
	if err != nil {
		panic(err)
	}

	err = ioutil.WriteFile("state.dat", b.Bytes(), 666)
	if err != nil {
		panic(err)
	}
	log.Println("saved state")
}

func (s *socketServer) loadState() {
	if _, err := os.Stat("state.dat"); os.IsNotExist(err) {
		log.Println("hey, tried to load a state that doesn't exist")
		return
	}
	raw, err := ioutil.ReadFile("state.dat")
	if err != nil {
		panic(err)
	}
	b := bytes.NewBuffer(raw)
	d := gob.NewDecoder(b)

	var m map[int]map[int]int
	err = d.Decode(&m)
	if err != nil {
		panic(err)
	}
	s.shardCache = m

	log.Println("loaded state")
}
