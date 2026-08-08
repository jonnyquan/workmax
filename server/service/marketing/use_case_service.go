package marketing

import (
	"server/globals"
	"server/model"
	"server/model/common/request"
)

type UseCaseService struct{}

// CreateUseCase creates a new UseCase
func (s *UseCaseService) CreateUseCase(useCase model.UseCase) (err error) {
	err = globals.GraDBs["system"].Create(&useCase).Error
	return err
}

// DeleteUseCase deletes a UseCase by ID
func (s *UseCaseService) DeleteUseCase(useCase model.UseCase) (err error) {
	err = globals.GraDBs["system"].Delete(&useCase).Error
	return err
}

// DeleteUseCaseByIds deletes UseCases by IDs
func (s *UseCaseService) DeleteUseCaseByIds(ids []int) (err error) {
	err = globals.GraDBs["system"].Delete(&[]model.UseCase{}, "id in ?", ids).Error
	return err
}

// UpdateUseCase updates an existing UseCase
func (s *UseCaseService) UpdateUseCase(useCase model.UseCase) (err error) {
	err = globals.GraDBs["system"].Save(&useCase).Error
	return err
}

// GetUseCase gets a UseCase by ID
func (s *UseCaseService) GetUseCase(id uint) (useCase model.UseCase, err error) {
	err = globals.GraDBs["system"].Where("id = ?", id).First(&useCase).Error
	return
}

// GetUseCaseBySlug gets a UseCase by Slug and Lang
func (s *UseCaseService) GetUseCaseBySlug(slug string, lang string) (useCase model.UseCase, err error) {
	err = globals.GraDBs["system"].Where("slug = ? AND lang = ? AND status = 1", slug, lang).First(&useCase).Error
	return
}

// GetUseCaseList gets a list of UseCases with pagination
func (s *UseCaseService) GetUseCaseList(info request.PageInfo, lang string, keyword string, appSlug string) (list interface{}, total int64, err error) {
	limit := info.PageSize
	offset := info.PageSize * (info.Page - 1)
	db := globals.GraDBs["system"].Model(&model.UseCase{})
	var useCases []model.UseCase

	// Only show published use cases
	db = db.Where("status = ?", 1)

	if lang != "" {
		db = db.Where("lang = ?", lang)
	}

	if keyword != "" {
		db = db.Where("title LIKE ?", "%"+keyword+"%")
	}

	if appSlug != "" {
		db = db.Where("app_slug = ?", appSlug)
	}

	err = db.Count(&total).Error
	if err != nil {
		return
	}
	err = db.Order("published_at desc").Limit(limit).Offset(offset).Find(&useCases).Error
	return useCases, total, err
}

// GetUseCasesByAppSlug gets use cases for a specific app
func (s *UseCaseService) GetUseCasesByAppSlug(appSlug string, lang string, limit int) (useCases []model.UseCase, err error) {
	db := globals.GraDBs["system"].Model(&model.UseCase{})
	db = db.Where("status = ? AND app_slug = ?", 1, appSlug)
	if lang != "" {
		db = db.Where("lang = ?", lang)
	}
	err = db.Order("published_at desc").Limit(limit).Find(&useCases).Error
	return
}
