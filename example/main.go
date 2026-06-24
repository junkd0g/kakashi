package main

import (
	"context"
	"fmt"

	"github.com/junkd0g/kakashi"
)

type Post struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

var GetPost1 = kakashi.NewEndpoint[Post](kakashi.MethodGet, "/posts/1")

func main() {
	apiClient := kakashi.New("https://jsonplaceholder.typicode.com")

	fmt.Println("Fetching post 1...")

	post, err := kakashi.Call(context.Background(), apiClient, GetPost1)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Success! Title: %s\n", post.Title)
}
