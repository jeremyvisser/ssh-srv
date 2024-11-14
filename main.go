package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const introText = `SUMMARY

	Resolves an _ssh._tcp SRV record, and passes the socket to SSH via ProxyUseFdPass.

USAGE

		%[1]s HOSTNAME [PORT]

	The socket is handed to fd 1 using ancilliary data.

	Port is optional, and only used in the case of non-SRV fallback.
	If SRV records are found, the port from the SRV is used instead.

EXAMPLES

	ssh -o ProxyUseFdPass=yes -o ProxyCommand='%[1]s %%h %%p' user@hostname

	# ~/.ssh/ssh_config
	Host *.mydomain.invalid
		ProxyUseFdPass  yes
		ProxyCommand    %[1]s %%h %%p
`

const (
	connTimeout = 1 * time.Minute
	connRace    = 300 * time.Millisecond
)

func Race[T any](ctx context.Context, next []func(context.Context) (T, error), interval time.Duration) (T, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c := make(chan T)
	defer close(c)

	var errv atomic.Value
	var wg sync.WaitGroup

	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()

		for _, n := range next {
			wg.Add(1)
			skip := make(chan struct{})
			go func() {
				defer wg.Done()
				defer close(skip)
				val, err := n(ctx)
				if err != nil {
					errv.CompareAndSwap(nil, err)
					return
				}
				c <- val
			}()

			select {
			case <-ctx.Done():
				// context cancelled, nothing more to do:
				return
			case <-t.C:
				// timer fired, try next option:
				continue
			case <-skip:
				// finished early, move to next without waiting for timer:
				t.Reset(interval)
				continue
			}
		}

		wg.Wait()
		cancel() // all jobs finished, no need to wait further
	}()

	select {
	case val := <-c:
		return val, nil
	case <-ctx.Done():
		return *new(T), fmt.Errorf("%v while waiting for result, but got: %w", ctx.Err(), errv.Load().(error))
	}
}

var ErrSRVLookup = errors.New("LookupSRV")

func DialSRV(service, proto, name string, peek func(net.Conn) error) (net.Conn, error) {
	cname, addrs, err := net.LookupSRV(service, proto, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSRVLookup, err)
	}
	log.Printf("%d SRV records found for %s", len(addrs), cname)

	var d net.Dialer
	var tryAddr []func(context.Context) (net.Conn, error)

	for _, addr := range addrs {
		log.Printf("Resolved (prio %d, weight %d) %s:%d",
			addr.Priority, addr.Weight, addr.Target, addr.Port)

		tryAddr = append(tryAddr, func(ctx context.Context) (net.Conn, error) {
			log.Printf("Trying to connect: %s:%d", addr.Target, addr.Port)

			conn, err := d.DialContext(ctx, proto, net.JoinHostPort(addr.Target, strconv.Itoa(int(addr.Port))))
			if err != nil {
				return nil, err
			}
			log.Printf("Connected to %s", conn.RemoteAddr())

			if peek != nil {
				if err := peek(conn); err != nil {
					log.Printf("%s: peek: %s", conn.RemoteAddr(), err)
					return nil, err
				}
				log.Printf("Peek succeeded for %s", conn.RemoteAddr())
			}

			return conn, nil
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), connTimeout)
	defer cancel()

	return Race[net.Conn](ctx, tryAddr, connRace)
}

// peekSSH returns nil if Conn is an SSH connection.
// It uses MSG_PEEK, which doesn't advance the buffer, allowing the socket
// to be reused later.
func peekSSH(conn net.Conn) error {
	tc, ok := conn.(*net.TCPConn)
	if !ok {
		panic("peekSSH: conn is not a TCPConn")
	}

	fd, err := tc.File()
	if err != nil {
		return err
	}

	const wantStr = "SSH-2"
	buf := make([]byte, len(wantStr))
	n, _, err := syscall.Recvfrom(int(fd.Fd()), buf, syscall.MSG_PEEK|syscall.MSG_WAITALL)
	if err != nil || n < len(buf) {
		return fmt.Errorf("peekSSH: Recvfrom: len %d, err %w", n, err)
	}
	if string(buf) != wantStr {
		return fmt.Errorf("peekSSH: Recvfrom: wanted '%s', got (hex) '%x'", wantStr, buf)
	}

	return nil
}

func init() {
	log.SetFlags(0)
	log.SetPrefix(os.Args[0] + ": ")
}

func main() {
	if len(os.Args) < 2 || os.Args[1][0] == '-' {
		fmt.Fprintf(os.Stderr, introText, os.Args[0])
		os.Exit(1)
	}

	host := os.Args[1]
	fallbackPort := "22"
	if len(os.Args) >= 3 {
		fallbackPort = os.Args[2]
	}

	c, err := DialSRV("ssh", "tcp", os.Args[1], peekSSH)
	if err != nil {
		if errors.Is(err, ErrSRVLookup) {
			hostPort := net.JoinHostPort(host, fallbackPort)
			log.Print("Fallback to non-SRV: ", hostPort)
			if c, err = net.Dial("tcp", hostPort); err != nil {
				log.Fatal(err)
			}
		} else {
			log.Fatal(err)
		}
	}
	log.Print("DialSRV handed us ", c.RemoteAddr())

	conn, ok := c.(*net.TCPConn)
	if !ok {
		panic("conn is not a TCPConn")
	}

	fd, err := conn.File()
	if err != nil {
		log.Fatal(err)
	}

	ancdata := syscall.UnixRights(int(fd.Fd()))
	if err := syscall.Sendmsg(int(os.Stdout.Fd()),
		[]byte{0},
		ancdata,
		nil,
		0,
	); err != nil {
		log.Fatal("Failed handing socket to stdout: Sendmsg: ", err)
	}

	log.Println("Socket handed to stdout")
}
