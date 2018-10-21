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
