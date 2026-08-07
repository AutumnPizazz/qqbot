package civgo

import (
	"fmt"
	"os"
	"testing"
)

func TestSmokeRealDocs(t *testing.T) {
	dir := "testdata"
	files, err := scanDocs(dir)
	if err != nil || len(files) == 0 {
		t.Skipf("无真实文档: %v", err)
	}
	total := 0
	for _, f := range files {
		content, _ := os.ReadFile(dir + "/" + f)
		chunks := ChunkFile(f, string(content), 800, 100)
		total += len(chunks)
		fmt.Printf("  %-28s → %2d 块\n", f, len(chunks))
	}
	fmt.Printf("总计: %d 文件 → %d 块\n", len(files), total)
	if total < 100 {
		t.Errorf("分块数量异常少: %d", total)
	}
}
