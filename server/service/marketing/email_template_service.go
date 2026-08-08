package marketing

import (
	"server/globals"
	"server/model"
)

type EmailTemplateService struct{}

// GetTemplateList 获取模板列表
func (s *EmailTemplateService) GetTemplateList(page, pageSize int, category string, status *int, keyword string) ([]model.EmailTemplate, int64, error) {
	var templates []model.EmailTemplate
	var total int64

	db := globals.GraDBs["system"].Model(&model.EmailTemplate{})

	// 筛选条件
	if category != "" {
		db = db.Where("category = ?", category)
	}
	if status != nil {
		db = db.Where("status = ?", *status)
	}
	if keyword != "" {
		db = db.Where("name LIKE ? OR code LIKE ? OR subject LIKE ?",
			"%"+keyword+"%", "%"+keyword+"%", "%"+keyword+"%")
	}

	// 获取总数
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询
	offset := (page - 1) * pageSize
	if err := db.Order("id DESC").Offset(offset).Limit(pageSize).Find(&templates).Error; err != nil {
		return nil, 0, err
	}

	return templates, total, nil
}

// GetTemplateByID 根据ID获取模板
func (s *EmailTemplateService) GetTemplateByID(id int) (*model.EmailTemplate, error) {
	var template model.EmailTemplate
	if err := globals.GraDBs["system"].Where("id = ?", id).First(&template).Error; err != nil {
		return nil, err
	}
	return &template, nil
}

// GetTemplateByCode 根据代码获取模板
func (s *EmailTemplateService) GetTemplateByCode(code string) (*model.EmailTemplate, error) {
	var template model.EmailTemplate
	if err := globals.GraDBs["system"].Where("code = ? AND status = ?", code, model.EMAIL_TEMPLATE_STATUS_ENABLED).First(&template).Error; err != nil {
		return nil, err
	}
	return &template, nil
}

// CreateTemplate 创建模板
func (s *EmailTemplateService) CreateTemplate(template *model.EmailTemplate) error {
	return globals.GraDBs["system"].Create(template).Error
}

// UpdateTemplate 更新模板
func (s *EmailTemplateService) UpdateTemplate(template *model.EmailTemplate) error {
	return globals.GraDBs["system"].Model(&model.EmailTemplate{}).Where("id = ?", template.Id).Updates(template).Error
}

// DeleteTemplate 删除模板
func (s *EmailTemplateService) DeleteTemplate(id int) error {
	return globals.GraDBs["system"].Where("id = ?", id).Delete(&model.EmailTemplate{}).Error
}

// UpdateTemplateStatus 更新模板状态
func (s *EmailTemplateService) UpdateTemplateStatus(id int, status int) error {
	return globals.GraDBs["system"].Model(&model.EmailTemplate{}).Where("id = ?", id).Update("status", status).Error
}
