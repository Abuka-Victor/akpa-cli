package tcpclient

import (
	"fmt"
	"net"
	"time"
	// "os"
)

const DialTimeout = 10 * time.Second

func ConnectToServer(address string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", address, DialTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server: %v", err)
	}
	// fmt.Println("I tried to connect to: " + address)
	return conn, nil
}

func SendData(conn net.Conn, data []byte) error {
	_, err := conn.Write(data)
	if err != nil {
		return fmt.Errorf("failed to send data: %v", err)
	}
	return nil
}
