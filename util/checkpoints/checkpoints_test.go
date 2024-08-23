package checkpoints

import (
	"fmt"
	"testing"
	"time"
)

func TestBasic(t *testing.T) {
	c := New(func(name string) {
		t.Logf("checkpoint %s failed", name)
	})
	for i := 0; i < 5; i++ {
		fmt.Printf("i = %d\n", i)
		c.Check("c1", time.Second)
		time.Sleep(time.Second + 200*time.Millisecond)
	}
	c.Close()
}
