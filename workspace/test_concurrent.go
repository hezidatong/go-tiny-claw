package main

import (
	"fmt"
	"sync"
)

func main() {
	// 测试多次运行，确保结果稳定
	const testRuns = 10
	const goroutines = 1000

	for run := 1; run <= testRuns; run++ {
		var count int
		var wg sync.WaitGroup
		var mu sync.Mutex

		// 启动 1000 个 Goroutine 去并发累加
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				mu.Lock()
				count++
				mu.Unlock()
			}()
		}

		wg.Wait()
		
		fmt.Printf("第 %d 次运行 - 最终的 Count 是: %d (期望: %d)\n", run, count, goroutines)
		
		if count != goroutines {
			fmt.Printf("❌ 第 %d 次运行结果错误!\n", run)
			return
		}
	}
	
	fmt.Printf("✅ 所有 %d 次运行都正确，结果稳定为 %d\n", testRuns, goroutines)
}