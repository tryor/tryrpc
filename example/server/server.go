package main

import (
	"fmt"
	"net"

	"github.com/tryor/tryrpc"
)

func foo(test string, i int64, float float64, ints []int64, m map[string]string, intss [][]int64) {
	fmt.Println("foo: ", test, i, float, ints, m, intss)
}

func main() {

	l, err := net.Listen("tcp", ":50001")
	if err != nil {
		panic(err)
	}
	serv := tryrpc.NewServer()

	serv.Bind("foo", foo)
	serv.Bind("add", func(a int64, b int64) int64 {
		return a + b
	})
	serv.ListenAndRun(l)
}
