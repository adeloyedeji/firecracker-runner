//go:build linux

// Command fc-agent runs INSIDE the guest. It listens on a vsock port and
// answers every connection with "ready\n", giving the host an end-to-end
// "guest userspace is alive" signal after a snapshot restore.
//
// Listening vsock sockets survive snapshot/restore (open connections don't),
// so the host can connect immediately after resume.
//
// Stdlib only: the syscall package has no AF_VSOCK helpers, so bind/accept
// are issued as raw syscalls.
package main

import (
	"flag"
	"log"
	"syscall"
	"unsafe"
)

const (
	afVsock      = 40         // AF_VSOCK
	vmaddrCIDAny = 0xFFFFFFFF // VMADDR_CID_ANY
)

// struct sockaddr_vm from <linux/vm_sockets.h> (16 bytes).
type sockaddrVM struct {
	Family    uint16
	Reserved1 uint16
	Port      uint32
	CID       uint32
	Flags     uint8
	Zero      [3]uint8
}

func main() {
	port := flag.Uint("port", 1024, "vsock port to listen on")
	flag.Parse()

	fd, err := syscall.Socket(afVsock, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		log.Fatalf("socket: %v", err)
	}
	sa := sockaddrVM{Family: afVsock, Port: uint32(*port), CID: vmaddrCIDAny}
	if _, _, e := syscall.Syscall(syscall.SYS_BIND, uintptr(fd),
		uintptr(unsafe.Pointer(&sa)), unsafe.Sizeof(sa)); e != 0 {
		log.Fatalf("bind: %v", e)
	}
	if err := syscall.Listen(fd, 128); err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("fc-agent listening on vsock port %d", *port)

	msg := []byte("ready\n")
	for {
		// accept4(fd, NULL, NULL, SOCK_CLOEXEC): we don't need the peer address,
		// and syscall.Accept can't decode AF_VSOCK addresses anyway.
		nfd, _, e := syscall.Syscall6(syscall.SYS_ACCEPT4, uintptr(fd), 0, 0,
			syscall.SOCK_CLOEXEC, 0, 0)
		if e != 0 {
			if e != syscall.EINTR && e != syscall.ECONNABORTED {
				log.Printf("accept: %v", e)
			}
			continue
		}
		_, _ = syscall.Write(int(nfd), msg)
		_ = syscall.Close(int(nfd))
	}
}
