package main

import (
	"fmt"
	"sync"
)

func main() {
	// 全局计数器
	var count int
	var wg sync.WaitGroup
	var mu sync.Mutex // 添加互斥锁保护count

	// 启动 1000 个 Goroutine 去并发累加
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 使用互斥锁保护count的修改
			mu.Lock()
			count++
			mu.Unlock()
		}()
	}

	wg.Wait()
	fmt.Printf("最终的 Count 是: %d\n", count)
}
