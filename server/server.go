package server

import (
	"encoding/json"
	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"io/ioutil"
	"log"
	"net/http"
	"path"
	"strings"
)

// Server configures the HTTP server
type Server struct {
	Host   string
	Key    string
	Ips    []string
	socket *socketServer
}

// Run the HTTP server
func (s *Server) Run() error {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use()

	sock := newSocketServer()
	sock.loadState()
	go sock.run()
	s.socket = sock

	r.HandleFunc("/api/socket", sock.socketUpgrader)
	r.HandleFunc("/api/webhook/{key}", s.StatusWebhook)
	r.Handle("/*", http.FileServer(http.Dir("website")))

	return http.ListenAndServe(s.Host, r)
}

// StatusWebhook handles incoming status webhooks from the bots
func (s *Server) StatusWebhook(w http.ResponseWriter, r *http.Request) {

	isAllowed := false
	for _, v := range s.Ips {
		if strings.TrimSpace(v) == r.RemoteAddr {
			isAllowed = true
			break
		}
	}

	if isAllowed {
		w.WriteHeader(403)
		return
	}

	key := path.Base(r.URL.Path)
	if key != s.Key {
		w.WriteHeader(401)
		return
	}

	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Println(err)
		w.WriteHeader(500)
		return
	}
	var u ShardUpdate
	err = json.Unmarshal(b, &u)
	if err != nil {
		log.Println(err)
		w.WriteHeader(500)
		return
	}
	if u.Bot < 0 || u.Bot > s.socket.patronBots+1 {
		log.Println("invalid bot received?", u)
		w.WriteHeader(400)
		return
	}
	if u.ID < 0 || u.ID > s.socket.totalShards {
		log.Println("invalid shard received?", u)
		w.WriteHeader(400)
		return
	}
	if u.Bot > 0 && u.ID > s.socket.patronShards {
		log.Println("invalid shard received?", u)
		w.WriteHeader(400)
		return
	}

	s.socket.updateList <- u
	w.WriteHeader(202)
}
