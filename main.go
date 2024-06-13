package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var (
	addr    = flag.String("addr", "localhost:5008", "http service address")
	cmdPath string
	jar     = flag.String("jar", "", "-jar jar路劲")
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Maximum message size allowed from peer.
	maxMessageSize = 8192

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Time to wait before force close on connection.
	closeGracePeriod = 10 * time.Second
)

func pumpStdin(ws *websocket.Conn, w io.Writer) {
	defer ws.Close()

	for { // For each incoming message...
		_, message, err := ws.ReadMessage()
		if err != nil {
			break
		}
		message = append(message, '\r', '\n') // Payload should have CRLF at the end.

		// Write headers.
		_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(message))
		if err != nil {
			break
		}

		// Write payload.
		if _, err := w.Write(message); err != nil {
			break
		}
	}
}

func pumpStdout(ws *websocket.Conn, r io.Reader, done chan struct{}) {
	rd := bufio.NewReader(r)
L:
	for {
		var length int64
		for { // Read headers from stdout until empty line.
			line, err := rd.ReadString('\n')
			if err == io.EOF {
				break L
			}
			if line == "" {
				break
			}
			colon := strings.Index(line, ":")
			if colon < 0 {
				break
			}
			name, value := line[:colon], strings.TrimSpace(line[colon+1:])
			switch name {
			case "Content-Length":
				// Parse Content-Length header value.
				length, _ = strconv.ParseInt(value, 10, 32)
			}
		}

		// Read payload.
		data := make([]byte, length)
		io.ReadFull(rd, data)

		// Write payload to WebSocket.
		if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
			ws.Close()
			break
		}
	}
	close(done)

	ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	time.Sleep(closeGracePeriod)
	ws.Close()
}

func ping(ws *websocket.Conn, done chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait)); err != nil {
				log.Println("ping:", err)
			}
		case <-done:
			return
		}
	}
}

func internalError(ws *websocket.Conn, msg string, err error) {
	log.Println(msg, err)
	ws.WriteMessage(websocket.TextMessage, []byte("Internal server error."))
}

var upgrader = websocket.Upgrader{}

func serveWs(w http.ResponseWriter, r *http.Request) {
	upgrader.CheckOrigin = func(r *http.Request) bool { return true }
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade:", err)
		return
	}

	defer ws.Close()
	cmd := exec.Command("java", "-jar", *jar, "--stdio")
	inw, _ := cmd.StdinPipe()
	outr, _ := cmd.StdoutPipe()
	cmd.Start()

	done := make(chan struct{})
	go pumpStdout(ws, outr, done) // Read from stdout, write to WebSocket.
	go ping(ws, done)

	pumpStdin(ws, inw) // Read from WebSocket, write to stdin.

	// Some commands will exit when stdin is closed.
	inw.Close()

	// Other commands need a bonk on the head.
	cmd.Process.Signal(os.Interrupt)

	select {
	case <-done:
	case <-time.After(time.Second):
		// A bigger bonk on the head.
		cmd.Process.Signal(os.Kill)
		<-done
	}

	cmd.Process.Wait()
}

func main() {
	if len(os.Args) < 2 {
		fmt.Errorf("使用方法: lsp-proxy.exe -jar xx.jar -addr localhost:5008\\n lsp-proxy.exe -jar xx.jar")
		os.Exit(1)
	}

	flag.Parse()
	http.HandleFunc("/", serveWs)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
