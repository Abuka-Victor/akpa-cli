package main

import (
	tcpclient "akpa/cli/tcp_client"
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

func isDirectory(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, err
	}

	return fileInfo.IsDir(), err
}

func handleTunnelConnection(conn net.Conn) {
	defer conn.Close()

	// 1. Read the incoming raw HTTP request from the TCP socket
	reader := bufio.NewReader(conn)
	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			break
		}

		// 2. Rewrite the request URL so it points to your local file server
		req.URL.Scheme = "http"
		req.URL.Host = "localhost:5174"
		req.RequestURI = "" // Required when using client.Do() directly with absolute URLs

		// 3. Forward the request to your local HTTP server using an HTTP client
		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("Failed to forward request locally: %v\n", err)
			break
		}

		// 4. Write the local server's response back down the TCP connection
		err = resp.Write(conn)
		resp.Body.Close()
		if err != nil {
			fmt.Printf("Failed to write response back to TCP stream: %v\n", err)
			break
		}
	}
}

func main() {
	var argDir string = "."
	if len(os.Args) == 2 {
		argDir = os.Args[1]
	}
	fmt.Printf("The directory is: %v\n", argDir)
	flagDir, errFlag := isDirectory(argDir)
	if errFlag != nil {
		panic(errFlag)
	}
	fmt.Printf("Is directory: %v, Error: %v\n", flagDir, errFlag)
	fileServer := http.FileServer(http.Dir(argDir))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// No caches
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")

		// Process filename and path
		// If file download, if not display directory
		filename := filepath.Base(r.URL.Path)
		fullPath := filepath.Join(argDir, r.URL.Path)
		fmt.Printf("The filename is: %v\n", filename)
		if isAdirectory, err := isDirectory(fullPath); err == nil && !isAdirectory {
			w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
		}
		fileServer.ServeHTTP(w, r)
	})

	go func() {
		fmt.Println("Server started on http://localhost:5174")
		err := http.ListenAndServe(":5174", nil)
		if err != nil {
			panic(err)
		}
	}()

	conn, err := tcpclient.ConnectToServer("localhost:8080")
	if err != nil {
		fmt.Printf("Error connecting to socket server: %v\n", err)
		return
	}
	defer conn.Close()
	fmt.Println("Connected to socket server.")

	response, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		fmt.Println("Failed to receive message:", err)
		return
	}
	fmt.Print("Server echo: " + response)

	go handleTunnelConnection(conn)

	select {}
}
