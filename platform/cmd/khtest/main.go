package main

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func main() {
	path := os.Args[1]
	addr := os.Args[2]
	keyPath := os.Args[3]
	user := os.Args[4]

	cb, err := knownhosts.New(path)
	if err != nil {
		fmt.Println("parse knownhosts:", err)
		return
	}
	keyBytes, _ := os.ReadFile(keyPath)
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		fmt.Println("parse key:", err)
		return
	}
	var seen string
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			seen = ssh.FingerprintSHA256(key) + " (" + key.Type() + ")"
			return cb(hostname, remote, key)
		},
	}
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		fmt.Println("server offered:", seen)
		fmt.Printf("dial error (%T): %v\n", err, err)
		if ke, ok := err.(*knownhosts.KeyError); ok {
			fmt.Println("want:", len(ke.Want))
			for _, w := range ke.Want {
				fmt.Println("  want:", w.Filename, w.Line, ssh.FingerprintSHA256(w.Key), w.Key.Type())
			}
		}
		return
	}
	defer client.Close()
	fmt.Println("connected; server offered:", seen)
}
