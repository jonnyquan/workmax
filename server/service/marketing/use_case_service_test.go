package marketing

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"server/globals"
	"server/model"
	"server/model/common/request"
	"server/utils/testutil"
)

// use_case_service_test.go pins the load-bearing rule that drafts never
// leak to public surfaces. status=1 means published, status=2 means
// draft (per the comment on model.UseCase.Status). The three public-
// facing reads — GetUseCaseList, GetUseCaseBySlug, GetUseCasesByAppSlug
// — are consumed by the marketing site and feed sitemap-use-cases.xml,
// so any drift here would publish unfinished editorial content to
// Google + AI crawlers.

func swapTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testutil.NewTestDB(t)
	previous := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previous })
	return db
}

func seedUseCase(t *testing.T, db *gorm.DB, slug, lang string, status int, appSlug string) {
	t.Helper()
	row := model.UseCase{
		Slug:        slug,
		Title:       slug + " title",
		Summary:     slug + " summary",
		Lang:        lang,
		Status:      status,
		AppSlug:     appSlug,
		PublishedAt: time.Now().Add(-time.Hour),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed %s/%s: %v", slug, lang, err)
	}
}

func TestGetUseCaseList_excludesDrafts(t *testing.T) {
	db := swapTestDB(t)
	seedUseCase(t, db, "published-en", "en", 1, "")
	seedUseCase(t, db, "draft-en", "en", 2, "")

	svc := UseCaseService{}
	listIface, total, err := svc.GetUseCaseList(
		request.PageInfo{Page: 1, PageSize: 100},
		"en",
		"",
		"",
	)
	if err != nil {
		t.Fatalf("GetUseCaseList: %v", err)
	}
	if total != 1 {
		t.Errorf("total: got %d, want 1 (drafts must not count)", total)
	}
	list, ok := listIface.([]model.UseCase)
	if !ok {
		t.Fatalf("list type assertion failed")
	}
	if len(list) != 1 {
		t.Fatalf("len(list): got %d, want 1", len(list))
	}
	if list[0].Slug != "published-en" {
		t.Errorf("returned slug: got %q, want %q", list[0].Slug, "published-en")
	}
}

func TestGetUseCaseBySlug_rejectsDraft(t *testing.T) {
	db := swapTestDB(t)
	seedUseCase(t, db, "draft-only", "en", 2, "")

	svc := UseCaseService{}
	row, err := svc.GetUseCaseBySlug("draft-only", "en")
	if err == nil {
		t.Fatalf("expected error fetching draft, got row %+v", row)
	}
	// gorm.ErrRecordNotFound is the canonical "no published row" signal
	// the use-case page handler converts into a 404.
	if err != gorm.ErrRecordNotFound {
		t.Errorf("err: got %v, want ErrRecordNotFound", err)
	}
}

func TestGetUseCaseBySlug_returnsPublished(t *testing.T) {
	db := swapTestDB(t)
	seedUseCase(t, db, "live-page", "en", 1, "")

	svc := UseCaseService{}
	row, err := svc.GetUseCaseBySlug("live-page", "en")
	if err != nil {
		t.Fatalf("GetUseCaseBySlug: %v", err)
	}
	if row.Slug != "live-page" {
		t.Errorf("slug: got %q, want %q", row.Slug, "live-page")
	}
}

func TestGetUseCasesByAppSlug_excludesDrafts(t *testing.T) {
	db := swapTestDB(t)
	seedUseCase(t, db, "ok-1", "en", 1, "short-drama")
	seedUseCase(t, db, "draft", "en", 2, "short-drama")
	seedUseCase(t, db, "ok-2", "en", 1, "short-drama")
	seedUseCase(t, db, "other-app", "en", 1, "video-ad")

	svc := UseCaseService{}
	rows, err := svc.GetUseCasesByAppSlug("short-drama", "en", 100)
	if err != nil {
		t.Fatalf("GetUseCasesByAppSlug: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("len(rows): got %d, want 2 (drafts and other apps must be excluded)", len(rows))
	}
	for _, r := range rows {
		if r.Status != 1 {
			t.Errorf("returned non-published row: %+v", r)
		}
		if r.AppSlug != "short-drama" {
			t.Errorf("returned wrong app: %q", r.AppSlug)
		}
	}
}
