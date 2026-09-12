package service_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"miniaccount/internal/service"
)

// ★ 默认文件名不能爬出当前目录。
//
// 三处默认名曾经各写一遍清洗，其中两处只洗了单位名、
// 把**用户传进来的科目编码**原样拼进去：
//
//	SuggestedReconciliationName("../../../../tmp/pwned")
//	  → "../../../../tmp/pwned.xlsx"
//
// 前端「取默认名 → 直接导出」这条最自然的实现就会写到账套目录之外。
func TestSuggestedNamesRejectPathTraversal(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	evil := []string{
		"../../../../tmp/pwned",
		"/tmp/x",
		`..\..\x`,
		"a/b",
		"a:b",
	}
	for _, code := range evil {
		for name, fn := range map[string]func() (string, error){
			"recon":    func() (string, error) { return svc.SuggestedReconciliationName(ctx, code) },
			"columnar": func() (string, error) { return svc.SuggestedColumnarName(ctx, code) },
		} {
			got, err := fn()
			if err != nil {
				t.Fatalf("%s(%q) 报错: %v", name, code, err)
			}
			// 必须落在当前目录下，且不含任何分隔符
			if filepath.Dir(got) != "." {
				t.Errorf("★ %s(%q) = %q，越出了当前目录", name, code, got)
			}
			if strings.ContainsAny(filepath.Base(got), `/\:`) {
				t.Errorf("%s(%q) = %q，文件名里仍有分隔符", name, code, got)
			}
		}
	}
}

// 单位名含非法字符时也要清洗（这一项原本就是对的，防止回归）。
func TestSuggestedExportNameSanitizesCompany(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	got, err := svc.SuggestedExportName(ctx, service.ExportBalanceSheet, 2025, 3)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got) != "." {
		t.Errorf("默认导出名 = %q，越出了当前目录", got)
	}
}
