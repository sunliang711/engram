package memory

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSummarizeContent_StripsYAMLFrontmatter(t *testing.T) {
	md := `---
title: broker-core 环境配置
category: env
tags:
  - uat
  - deploy
updated_at: 2026-11-18
---

## 测试环境说明

这里是部署说明和注意事项，包含服务端口、域名与鉴权配置。`

	got := summarizeContent(md, "", 200)
	if strings.Contains(got, "updated_at:") || strings.Contains(got, "category: env") {
		t.Fatalf("expected YAML frontmatter to be stripped from summary, got: %q", got)
	}
	if !strings.Contains(got, "## 测试环境说明") || !strings.Contains(got, "服务端口") {
		t.Fatalf("expected body text in summary, got: %q", got)
	}
}

func TestSummarizeContent_ExtractsYAMLDescription(t *testing.T) {
	md := `---
title: Deploy Guide
description: "broker-core 各环境访问地址与部署入口汇总"
tags: [uat, prod]
---

详细步骤如下：请先确认 VPN 已连接。`

	got := summarizeContent(md, "", 200)
	if strings.Contains(got, "tags:") {
		t.Fatalf("expected YAML metadata stripped, got: %q", got)
	}
	if !strings.HasPrefix(got, "broker-core 各环境访问地址与部署入口汇总") {
		t.Fatalf("expected extracted description at start of summary, got: %q", got)
	}
	if !strings.Contains(got, "详细步骤如下") {
		t.Fatalf("expected body text following description, got: %q", got)
	}
}

func TestSummarizeContent_PreservesMarkdownHorizontalRule(t *testing.T) {
	md := "# Title\n\nFirst section\n\n---\n\nSecond section"
	got := summarizeContent(md, "", 200)
	if !strings.Contains(got, "First section") || !strings.Contains(got, "Second section") {
		t.Fatalf("expected markdown horizontal rule not to strip document, got: %q", got)
	}
}

func TestSummarizeContent_QuerySnippetNearMatch(t *testing.T) {
	filler := strings.Repeat("这是一段前置的背景介绍与历史变更说明，用于模拟较长文档的开头部分。", 12)
	md := "---\ntitle: 环境地址\ntags: [uat]\n---\n\n" +
		"项目地址总览：请按需要选择环境。\n\n" +
		filler + "\n\n" +
		"## 环境列表\n- UAT 地址：https://uat.broker-core.internal:8443\n- Prod 地址：https://prod.broker-core.internal\n"

	got := summarizeContent(md, "UAT 地址", 140)
	if strings.Contains(got, "tags: [uat]") {
		t.Fatalf("expected YAML frontmatter not to appear in snippet, got: %q", got)
	}
	if !strings.Contains(got, "https://uat.broker-core.internal:8443") {
		t.Fatalf("expected snippet around matched 'UAT 地址' to include URL, got: %q", got)
	}
	if !strings.HasPrefix(got, "...") {
		t.Fatalf("expected leading ellipsis when window starts mid-body, got: %q", got)
	}

	// Case-insensitive query should also match.
	gotCase := summarizeContent(md, "uat", 140)
	if !strings.Contains(gotCase, "https://uat.broker-core.internal:8443") {
		t.Fatalf("expected case-insensitive match snippet to include URL, got: %q", gotCase)
	}
}

func TestSummarizeContent_QueryNotInBodyFallsBackCleanly(t *testing.T) {
	md := "---\ntitle: 特殊标签文档\ndescription: \"仅在标题或标签命中的记录\"\n---\n\n正文只描述日常巡检流程。"
	got := summarizeContent(md, "不存在正文里的词", 200)
	if strings.Contains(got, "title:") {
		t.Fatalf("expected YAML stripped on fallback, got: %q", got)
	}
	if !strings.Contains(got, "仅在标题或标签命中的记录") || !strings.Contains(got, "日常巡检流程") {
		t.Fatalf("unexpected fallback summary: %q", got)
	}
}

func TestSummarizeContent_IndexSummaryLimit(t *testing.T) {
	longBody := strings.Repeat("架构规范与配置说明", 20)
	got := summarizeContent(longBody, "", indexSummaryMaxRunes)
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected truncated index summary with ellipsis, got %q", got)
	}
	if len([]rune(got)) != indexSummaryMaxRunes+3 {
		t.Fatalf("expected %d runes including ellipsis, got %d (%q)", indexSummaryMaxRunes+3, len([]rune(got)), got)
	}
}

func TestScopeIndexFilter_GuardsScopeAndProjectID(t *testing.T) {
	tenantID := uuid.New()
	projectID := uuid.New()

	where, args, err := scopeIndexFilter("global", tenantID, nil)
	if err != nil || !strings.Contains(where, "scope = 'global' AND project_id IS NULL") || len(args) != 1 {
		t.Fatalf("unexpected global filter: where=%q args=%v err=%v", where, args, err)
	}

	where, args, err = scopeIndexFilter("project", tenantID, &projectID)
	if err != nil || !strings.Contains(where, "scope = 'project' AND project_id = $2") || len(args) != 2 {
		t.Fatalf("unexpected project filter: where=%q args=%v err=%v", where, args, err)
	}

	if _, _, err := scopeIndexFilter("global", tenantID, &projectID); err == nil {
		t.Fatal("expected error when passing project_id to global scope index")
	}
	if _, _, err := scopeIndexFilter("project", tenantID, nil); err == nil {
		t.Fatal("expected error when omitting project_id for project scope index")
	}
	if _, _, err := scopeIndexFilter("invalid", tenantID, nil); err == nil {
		t.Fatal("expected error for invalid scope")
	}
}
