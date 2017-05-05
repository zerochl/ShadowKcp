// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package hello is a trivial package for gomobile bind example.
package kcp

import (
	"fmt"
	"log"
	"time"
)

func Greetings(name string) string {
	for i := 0; i < 3; i++ {
		time.Sleep(2 * time.Second)
		log.Println("sleep:", i)
	}
	return fmt.Sprintf("HelloVeryGood, %s!", name)
}
