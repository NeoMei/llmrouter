package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// Vendor 用于存储供应商信息，供模型引用
// Name 唯一，用于在模型中关联
// Icon 采用 @lobehub/icons 的图标名，前端可直接渲染
// Status 预留字段，1 表示启用
// 本表同样遵循 3NF 设计范式

type Vendor struct {
	Id          int            `json:"id"`
	Name        string         `json:"name" gorm:"size:128;not null;uniqueIndex:uk_vendor_name_delete_at,priority:1"`
	Description string         `json:"description,omitempty" gorm:"type:text"`
	Icon        string         `json:"icon,omitempty" gorm:"type:varchar(128)"`
	Status      int            `json:"status" gorm:"default:1"`
	CreatedTime int64          `json:"created_time" gorm:"bigint"`
	UpdatedTime int64          `json:"updated_time" gorm:"bigint"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index;uniqueIndex:uk_vendor_name_delete_at,priority:2"`
}

// Insert 创建新的供应商记录
func (v *Vendor) Insert() error {
	return v.insertWithDB(DB)
}

func (v *Vendor) insertWithDB(tx *gorm.DB) error {
	now := common.GetTimestamp()
	v.CreatedTime = now
	v.UpdatedTime = now
	return tx.Create(v).Error
}

// FindOrInsertWithDB reuses a configured supplier without changing its metadata.
func (v *Vendor) FindOrInsertWithDB(tx *gorm.DB) (bool, error) {
	var existing Vendor
	err := lockForUpdate(tx).Where("name = ?", v.Name).First(&existing).Error
	if err == nil {
		*v = existing
		return false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	if err := v.insertWithDB(tx); err != nil {
		return false, err
	}
	return true, nil
}

// IsVendorNameDuplicated 检查供应商名称是否重复（排除自身 ID）
func IsVendorNameDuplicated(id int, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	var cnt int64
	err := DB.Model(&Vendor{}).Where("name = ? AND id <> ?", name, id).Count(&cnt).Error
	return cnt > 0, err
}

// Update 更新供应商记录
func (v *Vendor) Update() error {
	v.UpdatedTime = common.GetTimestamp()
	return DB.Model(&Vendor{}).Where("id = ?", v.Id).
		Select("name", "description", "icon", "status", "updated_time").Updates(v).Error
}

// Delete 软删除供应商
func (v *Vendor) Delete() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := lockModelVendor(tx, v.Id); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&Model{}).Where("vendor_id = ?", v.Id).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("vendor is referenced by %d model metadata records", count)
		}
		return tx.Delete(v).Error
	})
}

// lockModelVendor serializes changes to model references with vendor deletion.
func lockModelVendor(tx *gorm.DB, id int) error {
	if id == 0 {
		return nil
	}
	var vendor Vendor
	err := lockForUpdate(tx).Select("id").Where("id = ?", id).First(&vendor).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("vendor %d does not exist", id)
	}
	return err
}

// GetVendorByID 根据 ID 获取供应商
func GetVendorByID(id int) (*Vendor, error) {
	var v Vendor
	err := DB.First(&v, id).Error
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// GetAllVendors 获取全部供应商（分页）
func GetAllVendors(offset int, limit int) ([]*Vendor, error) {
	var vendors []*Vendor
	err := DB.Offset(offset).Limit(limit).Find(&vendors).Error
	return vendors, err
}

// SearchVendors 按关键字搜索供应商
func SearchVendors(keyword string, offset int, limit int) ([]*Vendor, int64, error) {
	db := DB.Model(&Vendor{})
	if keyword != "" {
		like := "%" + keyword + "%"
		db = db.Where("name LIKE ? OR description LIKE ?", like, like)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var vendors []*Vendor
	if err := db.Offset(offset).Limit(limit).Order("id DESC").Find(&vendors).Error; err != nil {
		return nil, 0, err
	}
	return vendors, total, nil
}
