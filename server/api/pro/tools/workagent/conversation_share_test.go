package workagent

import (
	"encoding/json"
	"net/http"
	"testing"

	"server/model"
	workagentModel "server/model/workagent"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func buildConversationShareEngine(_ *testing.T) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.GET("/share/conversations/:threadId", api.GetConversationDetail)
	r.GET("/share/messages/:msgId", api.GetMessageDetail)
	return r
}

func TestGetConversationDetail_ReturnsOwnerAvatar(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	user := model.User{
		Nickname: "Ada",
		Avatar:   "https://cdn.example.test/ada.png",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	thread := seedConversationThread(t, db, user.Id, "Shared design review")
	thread.IsPublic = true
	if err := db.Save(thread).Error; err != nil {
		t.Fatalf("publish thread: %v", err)
	}
	if err := db.Create(&workagentModel.ChatMessage{
		UID:      int(user.Id),
		UUID:     uuid.New().String(),
		ThreadID: int(thread.Id),
		UserText: "Please review this banner.",
		AIText:   "The visual hierarchy is clear.",
		ChatMode: "agent",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}

	w := getRequest(buildConversationShareEngine(t), "/share/conversations/"+thread.UUID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Owner struct {
				Name   string `json:"name"`
				Avatar string `json:"avatar"`
			} `json:"owner"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if body.Data.Owner.Name != "Ada" || body.Data.Owner.Avatar != user.Avatar {
		t.Fatalf("owner = %+v, want name/avatar from w_user", body.Data.Owner)
	}
}
