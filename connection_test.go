// Copyright (c) 2021 VMware, Inc. or its affiliates. All Rights Reserved.
// Copyright (c) 2012-2021, Sean Treadway, SoundCloud Ltd.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build integration
// +build integration

package amqp091

import (
	"crypto/tls"
	"net"
	"sync"
	"testing"
	"time"
)

func TestRequiredServerLocale(t *testing.T) {
	conn := integrationConnection(t, "AMQP 0-9-1 required server locale")
	t.Cleanup(func() { conn.Close() })
	requiredServerLocale := defaultLocale

	for _, locale := range conn.Locales {
		if locale == requiredServerLocale {
			return
		}
	}

	t.Fatalf("AMQP 0-9-1 server must support at least the %s locale, server sent the following locales: %#v", requiredServerLocale, conn.Locales)
}

func TestDefaultConnectionLocale(t *testing.T) {
	conn := integrationConnection(t, "client default locale")
	t.Cleanup(func() { conn.Close() })

	if conn.Config.Locale != defaultLocale {
		t.Fatalf("Expected default connection locale to be %s, is was: %s", defaultLocale, conn.Config.Locale)
	}
}

func TestChannelOpenOnAClosedConnectionFails(t *testing.T) {
	conn := integrationConnection(t, "channel on close")

	conn.Close()

	if _, err := conn.Channel(); err != ErrClosed {
		t.Fatalf("channel.open on a closed connection %#v is expected to fail", conn)
	}
}

// TestChannelOpenOnAClosedConnectionFails_ReleasesAllocatedChannel ensures the
// channel allocated is released if opening the channel fails.
func TestChannelOpenOnAClosedConnectionFails_ReleasesAllocatedChannel(t *testing.T) {
	conn := integrationConnection(t, "releases channel allocation")
	conn.Close()

	before := len(conn.channels)

	if _, err := conn.Channel(); err != ErrClosed {
		t.Fatalf("channel.open on a closed connection %#v is expected to fail", conn)
	}

	if len(conn.channels) != before {
		t.Fatalf("channel.open failed, but the allocated channel was not released")
	}
}

// TestRaceBetweenChannelAndConnectionClose ensures allocating a new channel
// does not race with shutting the connection down.
//
// See https://github.com/streadway/amqp/issues/251 - thanks to jmalloc for the
// test case.
func TestRaceBetweenChannelAndConnectionClose(t *testing.T) {
	defer time.AfterFunc(10*time.Second, func() { t.Fatalf("Close deadlock") }).Stop()

	conn := integrationConnection(t, "allocation/shutdown race")

	go conn.Close()
	for i := 0; i < 10; i++ {
		go func() {
			ch, err := conn.Channel()
			if err == nil {
				ch.Close()
			}
		}()
	}
}

// TestRaceBetweenChannelShutdownAndSend ensures closing a channel
// (channel.shutdown) does not race with calling channel.send() from any other
// goroutines.
//
// See https://github.com/streadway/amqp/pull/253#issuecomment-292464811 for
// more details - thanks to jmalloc again.
func TestRaceBetweenChannelShutdownAndSend(t *testing.T) {
	const concurrency = 10
	defer time.AfterFunc(10*time.Second, func() { t.Fatalf("Close deadlock") }).Stop()

	conn := integrationConnection(t, "channel close/send race")
	defer conn.Close()

	ch, _ := conn.Channel()
	go ch.Close()

	errs := make(chan error, concurrency)
	wg := sync.WaitGroup{}
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			// ch.Ack calls ch.send() internally.
			if err := ch.Ack(42, false); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Logf("[INFO] %#v (%s) of type %T", err, err, err)
		}
	}
}

func TestQueueDeclareOnAClosedConnectionFails(t *testing.T) {
	conn := integrationConnection(t, "queue declare on close")
	ch, _ := conn.Channel()

	conn.Close()

	if _, err := ch.QueueDeclare("an example", false, false, false, false, nil); err != ErrClosed {
		t.Fatalf("queue.declare on a closed connection %#v is expected to return ErrClosed, returned: %#v", conn, err)
	}
}

func TestConcurrentClose(t *testing.T) {
	const concurrency = 32

	conn := integrationConnection(t, "concurrent close")
	defer conn.Close()

	errs := make(chan error, concurrency)
	wg := sync.WaitGroup{}
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()

			err := conn.Close()

			if err == nil {
				t.Log("first concurrent close was successful")
				return
			}

			if err == ErrClosed {
				t.Log("later concurrent close were successful and returned ErrClosed")
				return
			}

			// BUG(st) is this really acceptable? we got a net.OpError before the
			// connection was marked as closed means a race condition between the
			// network connection and handshake state. It should be a package error
			// returned.
			if _, neterr := err.(*net.OpError); neterr {
				t.Logf("unknown net.OpError during close, ignoring: %+v", err)
				return
			}

			// A different/protocol error occurred indicating a race or missed condition
			if _, other := err.(*Error); other {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("Expected no error, or ErrClosed, or a net.OpError from conn.Close(), got %#v (%s) of type %T", err, err, err)
		}
	}
}

// TestPlaintextDialTLS ensures amqp:// connections succeed when using DialTLS.
func TestPlaintextDialTLS(t *testing.T) {
	uri, err := ParseURI(amqpURL)
	if err != nil {
		t.Fatalf("parse URI error: %s", err)
	}

	// We can only test when we have a plaintext listener
	if uri.Scheme != "amqp" {
		t.Skip("requires server listening for plaintext connections")
	}

	conn, err := DialTLS(uri.String(), &tls.Config{MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("unexpected dial error, got %v", err)
	}
	conn.Close()
}

// TestIsClosed will test the public method IsClosed on a connection.
func TestIsClosed(t *testing.T) {
	conn := integrationConnection(t, "public IsClosed()")

	if conn.IsClosed() {
		t.Fatalf("connection expected to not be marked as closed")
	}

	conn.Close()

	if !conn.IsClosed() {
		t.Fatal("connection expected to be marked as closed")
	}
}

// TestChannelIsClosed will test the public method IsClosed on a channel.
func TestChannelIsClosed(t *testing.T) {
	conn := integrationConnection(t, "public channel.IsClosed()")
	t.Cleanup(func() { conn.Close() })
	ch, _ := conn.Channel()

	if ch.IsClosed() {
		t.Fatalf("channel expected to not be marked as closed")
	}

	ch.Close()

	if !ch.IsClosed() {
		t.Fatal("channel expected to be marked as closed")
	}
}

// TestReaderGoRoutineTerminatesWhenMsgIsProcessedDuringClose tests the issue
// described in https://github.com/rabbitmq/amqp091-go/issues/69.
func TestReaderGoRoutineTerminatesWhenMsgIsProcessedDuringClose(t *testing.T) {
	const routines = 10
	c := integrationConnection(t, t.Name())

	var wg sync.WaitGroup
	startSigCh := make(chan interface{})

	for i := 0; i < routines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			<-startSigCh

			err := c.Close()
			if err != nil {
				t.Logf("close failed in routine %d: %s", id, err.Error())
			}
		}(i)
	}
	close(startSigCh)

	t.Log("waiting for go-routines to terminate")
	wg.Wait()
}
