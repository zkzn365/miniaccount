package evidence_test

import (
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/evidence"
)

func file(owner evidence.OwnerType, ownerID int64, hash, name, note string) evidence.Link {
	return evidence.Link{
		OwnerType: owner, OwnerID: ownerID,
		RefKind: evidence.RefAttachment, SHA256: hash, FileName: name,
		RefLabel: name, Note: note,
	}
}

func voucherLink(owner evidence.OwnerType, ownerID, vid int64, label string) evidence.Link {
	return evidence.Link{
		OwnerType: owner, OwnerID: ownerID,
		RefKind: evidence.RefVoucher, RefID: vid, RefLabel: label,
	}
}

// 一条证据必须说得清「这是什么资料」。
//
// ★ 这一条挡的是最常见的一种敷衍：挂个文件上去就以为有证据了。
// 「依据：扫描件.pdf」本身说明不了什么，复核人要的是
// 「这份表说明了什么」。
func TestLinkValidate(t *testing.T) {
	ok := file(evidence.OwnerAdjustment, 1, strings.Repeat("a", 64), "折旧计算表.xlsx", "少提折旧 3,000.00")
	if err := ok.Validate(); err != nil {
		t.Fatalf("基准用例应当通过：%v", err)
	}

	cases := []struct {
		name string
		mod  func(l *evidence.Link)
		want string
	}{
		{"结论种类不认识", func(l *evidence.Link) { l.OwnerType = "vibes" }, "结论种类"},
		{"结论 id 为零", func(l *evidence.Link) { l.OwnerID = 0 }, "结论 id"},
		{"证据种类不认识", func(l *evidence.Link) { l.RefKind = "photo" }, "证据种类"},
		{"附件没有文件", func(l *evidence.Link) { l.SHA256 = "" }, "必须带有文件"},
		{"附件没有文件名", func(l *evidence.Link) { l.FileName = "" }, "文件名"},
		{"凭证没给 id", func(l *evidence.Link) {
			l.RefKind = evidence.RefVoucher
			l.SHA256, l.FileName = "", ""
			l.RefID = 0
		}, "单据 id"},
		{"什么都没写", func(l *evidence.Link) { l.RefLabel, l.Note = "", "" }, "要写清"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := ok
			c.mod(&l)
			err := l.Validate()
			if err == nil {
				t.Fatal("应当被拦下")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("报错应当提到 %q，实际：%v", c.want, err)
			}
		})
	}
}

// 外部资料（纸质回函、访谈记录）没有文件也没有单据 id，但要能登记 ——
// 审计现场大量证据本来就没有电子件，不认它就等于逼着人把资料
// 描述成别的东西挂上去。
func TestExternalEvidenceIsAllowed(t *testing.T) {
	l := evidence.Link{
		OwnerType: evidence.OwnerConclusion, OwnerID: 1,
		RefKind: evidence.RefExternal, RefLabel: "应收账款函证回函（纸质，已归档）",
		Note: "回函金额与账面一致",
	}
	if err := l.Validate(); err != nil {
		t.Fatalf("外部资料应当可以登记：%v", err)
	}
}

// 同一份资料挂两次不该算两份证据。
func TestLinkIdentityDedupe(t *testing.T) {
	a := file(evidence.OwnerAdjustment, 7, strings.Repeat("b", 64), "折旧表.xlsx", "少提")
	b := file(evidence.OwnerAdjustment, 7, strings.Repeat("b", 64), "折旧表.xlsx", "少提")
	if a.Identity() != b.Identity() {
		t.Error("同一份附件挂到同一个结论下，判重键应当相同")
	}
	// 换个结论或换份文件就不是同一条
	c := file(evidence.OwnerAdjustment, 8, strings.Repeat("b", 64), "折旧表.xlsx", "少提")
	if a.Identity() == c.Identity() {
		t.Error("不同结论下的同一份文件是两条独立的关联")
	}
	d := voucherLink(evidence.OwnerAdjustment, 7, 12, "记-2025-03-0007")
	e := voucherLink(evidence.OwnerAdjustment, 7, 13, "记-2025-03-0008")
	if d.Identity() == e.Identity() {
		t.Error("两张不同的凭证是两条关联")
	}
}

func TestChainDescribesEvidence(t *testing.T) {
	c := evidence.Chain{
		OwnerType: evidence.OwnerAdjustment, OwnerID: 1, Title: "ADJ-202503-001",
		Links: []evidence.Link{
			file(evidence.OwnerAdjustment, 1, strings.Repeat("c", 64), "折旧计算表.xlsx", "少提"),
			voucherLink(evidence.OwnerAdjustment, 1, 7, "记-2025-03-0007"),
		},
	}
	if c.Total() != 2 || c.Files() != 1 || c.Documents() != 1 {
		t.Errorf("份数统计不对：共 %d（附件 %d / 单据 %d）",
			c.Total(), c.Files(), c.Documents())
	}
	if c.Empty() {
		t.Error("有两份依据，不该报空")
	}
	if !strings.Contains(c.Concludes(), "依据齐全") {
		t.Errorf("结论 = %q", c.Concludes())
	}
}

// ★ 没有证据与证据丢了，说的必须是两句不同的话。
//
// 前者是活没干完，后者是干了但资料没保住 —— 补的方式完全不同。
func TestChainDistinguishesMissingFromEmpty(t *testing.T) {
	empty := evidence.Chain{OwnerType: evidence.OwnerAdjustment, OwnerID: 1}
	if !empty.Empty() {
		t.Error("一条证据都没有时应当报 Empty")
	}
	if !strings.Contains(empty.Concludes(), "还没有附任何依据") {
		t.Errorf("没有证据时的话不对：%q", empty.Concludes())
	}

	broken := evidence.Chain{
		OwnerType: evidence.OwnerAdjustment, OwnerID: 1,
		Links: []evidence.Link{
			file(evidence.OwnerAdjustment, 1, strings.Repeat("d", 64), "折旧表.xlsx", "少提"),
			file(evidence.OwnerAdjustment, 1, strings.Repeat("e", 64), "发票.pdf", "补提依据"),
		},
	}
	broken.Links[1].Missing = true
	if broken.Empty() {
		t.Error("有两条关联，不该报空")
	}
	if len(broken.Missing()) != 1 {
		t.Fatalf("应当有 1 份找不到，实际 %d 份", len(broken.Missing()))
	}
	if !strings.Contains(broken.Concludes(), "找不到了") {
		t.Errorf("证据丢失时的话不对：%q", broken.Concludes())
	}
	if strings.Contains(broken.Concludes(), "还没有附任何依据") {
		t.Error("证据丢失不能被说成「没有证据」—— 补的方式不一样")
	}
}

func TestChainSetConcludes(t *testing.T) {
	full := evidence.Chain{
		OwnerType: evidence.OwnerAdjustment, OwnerID: 1, Title: "有依据的",
		Links: []evidence.Link{
			voucherLink(evidence.OwnerAdjustment, 1, 7, "记-2025-03-0007"),
		},
	}
	none := evidence.Chain{
		OwnerType: evidence.OwnerMateriality, OwnerID: 1, Title: "没依据的",
	}

	cases := []struct {
		name   string
		chains []evidence.Chain
		want   string
	}{
		{"都齐", []evidence.Chain{full}, "依据都齐"},
		{"有一项没依据", []evidence.Chain{full, none}, "还没有附依据"},
		{"没有需要举证的", nil, "还没有需要举证的结论"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := evidence.ChainSet{Period: "2025-03", Chains: c.chains}
			if !strings.Contains(set.Concludes(), c.want) {
				t.Errorf("结论 = %q，期望含 %q", set.Concludes(), c.want)
			}
		})
	}

	// 两种问题同时存在时要一起说出来
	broken := full
	broken.Links = append([]evidence.Link(nil), full.Links...)
	broken.Links[0].Missing = true
	set := evidence.ChainSet{Chains: []evidence.Chain{broken, none}}
	if len(set.Unsupported()) != 1 || len(set.Broken()) != 1 {
		t.Fatalf("应当各有一项：没依据 %d、丢了 %d",
			len(set.Unsupported()), len(set.Broken()))
	}
	msg := set.Concludes()
	if !strings.Contains(msg, "还没有依据") || !strings.Contains(msg, "找不到") {
		t.Errorf("两类问题都该说出来：%q", msg)
	}
}

func TestNewLinkStampsWhoAndWhen(t *testing.T) {
	at := time.Date(2025, 3, 31, 10, 30, 0, 0, time.UTC)
	l := evidence.NewLink(
		voucherLink(evidence.OwnerAdjustment, 1, 7, "记-2025-03-0007"),
		"  李审计  ", at)
	if l.LinkedBy != "李审计" {
		t.Errorf("操作人应当去掉两边空白，实际 %q", l.LinkedBy)
	}
	if !strings.HasPrefix(l.LinkedAt, "2025-03-31T10:30:00") {
		t.Errorf("时间戳 = %q", l.LinkedAt)
	}
}
