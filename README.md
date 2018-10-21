# tryrpc
RPC  framework based on msgpack


## Installation
To install:

    go get -u github.com/tryor/tryrpc

# Look&feel

## Server

```go
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
```

## Client

```go
package main

import (
	"fmt"

	"github.com/tryor/tryrpc"
)

func main() {
	client, err := tryrpc.NewClient("tcp", "127.0.0.1:50000;127.0.0.1:50001", 1, 10)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	err = client.Send("foo", "hello world!", 250, float64(345.23), []int64{1, 2, 3, 4}, map[string]string{"aaa": "111", "bbb": "222"}, [][]int64{{11, 22, 33}, {111, 222, 333}})
	fmt.Printf("foo() => err:%v\n", err)
	rs, err := client.Call("add", int64(3), int64(5))
	fmt.Printf("add(3,5) => %v, err:%v\n", rs, err)

}

```

