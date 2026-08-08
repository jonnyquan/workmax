package marketing

import (
	"encoding/json"
	"server/globals"
	"server/model"
	"time"

	"gorm.io/gorm"
)

type EmailSegmentService struct{}

// SegmentRule 分组规则结构
type SegmentRule struct {
	Field    string      `json:"field"`    // 字段名：member_status/registration_date/last_login等
	Operator string      `json:"operator"` // 操作符：eq/neq/gt/lt/gte/lte/in/nin/contains
	Value    interface{} `json:"value"`    // 值
}

type SegmentRules struct {
	Logic string        `json:"logic"` // and/or
	Rules []SegmentRule `json:"rules"`
}

// GetSegmentList 获取分组列表
func (s *EmailSegmentService) GetSegmentList(page, pageSize int, status *int, keyword string) ([]model.EmailSegment, int64, error) {
	var segments []model.EmailSegment
	var total int64

	db := globals.GraDBs["system"].Model(&model.EmailSegment{})

	// 筛选条件
	if status != nil {
		db = db.Where("status = ?", *status)
	}
	if keyword != "" {
		db = db.Where("name LIKE ? OR description LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}

	// 获取总数
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询
	offset := (page - 1) * pageSize
	if err := db.Order("id DESC").Offset(offset).Limit(pageSize).Find(&segments).Error; err != nil {
		return nil, 0, err
	}

	return segments, total, nil
}

// GetSegmentByID 根据ID获取分组
func (s *EmailSegmentService) GetSegmentByID(id int) (*model.EmailSegment, error) {
	var segment model.EmailSegment
	if err := globals.GraDBs["system"].Where("id = ?", id).First(&segment).Error; err != nil {
		return nil, err
	}
	return &segment, nil
}

// CreateSegment 创建分组
func (s *EmailSegmentService) CreateSegment(segment *model.EmailSegment) error {
	return globals.GraDBs["system"].Create(segment).Error
}

// UpdateSegment 更新分组
func (s *EmailSegmentService) UpdateSegment(segment *model.EmailSegment) error {
	return globals.GraDBs["system"].Model(&model.EmailSegment{}).Where("id = ?", segment.Id).Updates(segment).Error
}

// DeleteSegment 删除分组
func (s *EmailSegmentService) DeleteSegment(id int) error {
	return globals.GraDBs["system"].Where("id = ?", id).Delete(&model.EmailSegment{}).Error
}

// GetSegmentUsers 获取分组用户列表（无分页，用于同步）
func (s *EmailSegmentService) GetSegmentUsers(segmentID int) ([]model.User, error) {
	segment, err := s.GetSegmentByID(segmentID)
	if err != nil {
		return nil, err
	}

	var rules SegmentRules
	if err := json.Unmarshal([]byte(segment.Rules), &rules); err != nil {
		return nil, err
	}

	return s.FilterUsersByRules(rules, 0, 0)
}

// GetSegmentUsersWithPagination 获取分组用户列表（带分页）
func (s *EmailSegmentService) GetSegmentUsersWithPagination(segmentID int, page, pageSize int) ([]model.User, int64, error) {
	segment, err := s.GetSegmentByID(segmentID)
	if err != nil {
		return nil, 0, err
	}

	var rules SegmentRules
	if err := json.Unmarshal([]byte(segment.Rules), &rules); err != nil {
		return nil, 0, err
	}

	return s.FilterUsersByRulesWithPagination(rules, page, pageSize)
}

// FilterUsersByRules 根据规则筛选用户（无分页）
func (s *EmailSegmentService) FilterUsersByRules(rules SegmentRules, page, pageSize int) ([]model.User, error) {
	var users []model.User
	db := globals.GraDBs["system"].Model(&model.User{})

	// 应用规则
	db = s.buildQueryFromRules(db, rules)

	if err := db.Find(&users).Error; err != nil {
		return nil, err
	}

	return users, nil
}

// FilterUsersByRulesWithPagination 根据规则筛选用户（带分页）
func (s *EmailSegmentService) FilterUsersByRulesWithPagination(rules SegmentRules, page, pageSize int) ([]model.User, int64, error) {
	var users []model.User
	var total int64

	db := globals.GraDBs["system"].Model(&model.User{})

	// 应用规则
	db = s.buildQueryFromRules(db, rules)

	// 获取总数
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询
	offset := (page - 1) * pageSize
	if err := db.Offset(offset).Limit(pageSize).Find(&users).Error; err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

// buildQueryFromRules 根据规则构建查询
func (s *EmailSegmentService) buildQueryFromRules(db *gorm.DB, rules SegmentRules) *gorm.DB {
	for _, rule := range rules.Rules {
		switch rule.Field {
		case "member":
			db = s.applyCondition(db, "member", rule.Operator, rule.Value, rules.Logic)
		case "created_at":
			db = s.applyCondition(db, "created_at", rule.Operator, rule.Value, rules.Logic)
		case "login_time":
			db = s.applyCondition(db, "login_time", rule.Operator, rule.Value, rules.Logic)
		case "role":
			db = s.applyCondition(db, "role", rule.Operator, rule.Value, rules.Logic)
		case "ban":
			db = s.applyCondition(db, "ban", rule.Operator, rule.Value, rules.Logic)
		}
	}
	return db
}

// applyCondition 应用查询条件
func (s *EmailSegmentService) applyCondition(db *gorm.DB, field, operator string, value interface{}, logic string) *gorm.DB {
	switch operator {
	case "eq":
		db = db.Where(field+" = ?", value)
	case "neq":
		db = db.Where(field+" != ?", value)
	case "gt":
		db = db.Where(field+" > ?", value)
	case "lt":
		db = db.Where(field+" < ?", value)
	case "gte":
		db = db.Where(field+" >= ?", value)
	case "lte":
		db = db.Where(field+" <= ?", value)
	case "in":
		db = db.Where(field+" IN ?", value)
	case "nin":
		db = db.Where(field+" NOT IN ?", value)
	case "contains":
		db = db.Where(field+" LIKE ?", "%"+value.(string)+"%")
	}
	return db
}

// SyncSegmentUserCount 同步分组用户数量
func (s *EmailSegmentService) SyncSegmentUserCount(segmentID int) error {
	users, err := s.GetSegmentUsers(segmentID)
	if err != nil {
		return err
	}

	return globals.GraDBs["system"].Model(&model.EmailSegment{}).Where("id = ?", segmentID).Updates(map[string]interface{}{
		"user_count":     len(users),
		"last_sync_time": time.Now(),
	}).Error
}
