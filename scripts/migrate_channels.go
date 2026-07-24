//go:build ignore

// ponytail: 一次性迁移脚本，将现有 new-api 渠道导入 provider_profiles / provider_models / provider_key_pool。
//
// 用法:
//
//	go run scripts/migrate_channels.go
//
// 必需环境变量:
//
//	SQL_DSN          数据库连接串（同主程序；未设则回退到 SQLite one-api.db）
//	GATEWAY_AES_KEY  AES 加密密钥（16/24/32 字节，与主程序一致）
//
// 迁移范围: type ∈ {DeepSeek 43, Moonshot 25, Zhipu 16, ZhipuV4 26, Ali 17} 的渠道。
// 可重复执行 — 已迁移渠道（OtherSettings JSON 含 gateway_profile_id）会被跳过。
package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/crypto"

	"github.com/joho/godotenv"
)

// providerSpec describes the profile defaults for a given channel type.
type providerSpec struct {
	profileName string
	displayName string
	adapterType string
}

// ponytail: zhipu(16) 和 zhipu_v4(26) base_url 相同但 API 版本不同，拆为两个 profile。
var typeSpecs = map[int]providerSpec{
	constant.ChannelTypeDeepSeek: {"deepseek", "DeepSeek", "openai"},
	constant.ChannelTypeMoonshot: {"moonshot", "Moonshot (Kimi)", "openai"},
	constant.ChannelTypeZhipu:    {"zhipu", "Zhipu GLM", "zhipu"},
	constant.ChannelTypeZhipu_v4: {"zhipu_v4", "Zhipu GLM-4", "zhipuv4"},
	constant.ChannelTypeAli:      {"ali", "Alibaba Qwen", "ali"},
}

func main() {
	_ = godotenv.Load(".env")
	common.InitEnv()

	if err := model.InitDB(); err != nil {
		log.Fatalf("InitDB: %v", err)
	}

	aesKey := os.Getenv("GATEWAY_AES_KEY")
	if aesKey == "" {
		log.Fatal("GATEWAY_AES_KEY not set — cannot encrypt provider keys")
	}
	aes, err := crypto.NewAESGCM([]byte(aesKey))
	if err != nil {
		log.Fatalf("invalid GATEWAY_AES_KEY: %v", err)
	}

	targetTypes := make([]int, 0, len(typeSpecs))
	for t := range typeSpecs {
		targetTypes = append(targetTypes, t)
	}

	var channels []model.Channel
	if err := model.DB.Where("type IN (?)", targetTypes).Order("type, id").Find(&channels).Error; err != nil {
		log.Fatalf("query channels: %v", err)
	}

	fmt.Printf("Found %d candidate channels (types: deepseek/moonshot/zhipu/zhipu_v4/ali)\n\n", len(channels))

	// profileCache: "adapterType|baseURL" → *ProviderProfile (avoids repeated DB lookups).
	profileCache := make(map[string]*model.ProviderProfile)
	var migrated, skipped, keysInserted, modelsUpserted int

	for i := range channels {
		ch := &channels[i]
		spec, ok := typeSpecs[ch.Type]
		if !ok {
			continue
		}

		// Idempotency: skip channels that already carry a profile id.
		if pid := readProfileID(ch.OtherSettings); pid > 0 {
			skipped++
			fmt.Printf("  [SKIP] channel %d (%s) — already migrated to profile %d\n", ch.Id, ch.Name, pid)
			continue
		}

		baseURL := ch.GetBaseURL()
		if baseURL == "" {
			log.Printf("  [WARN] channel %d: empty base_url, skipping", ch.Id)
			continue
		}

		cacheKey := spec.adapterType + "|" + baseURL
		profile := profileCache[cacheKey]
		if profile == nil {
			profile = getOrCreateProfile(spec, baseURL)
			profileCache[cacheKey] = profile
		}

		// --- Keys → provider_key_pool ---
		for _, rawKey := range ch.GetKeys() {
			if strings.TrimSpace(rawKey) == "" {
				continue
			}
			enc, err := aes.Encrypt([]byte(rawKey))
			if err != nil {
				log.Printf("  [WARN] channel %d: encrypt key failed: %v", ch.Id, err)
				continue
			}
			poolKey := model.ProviderKeyPool{
				ProfileId:    profile.Id,
				EncryptedKey: enc,
				Status:       "active",
			}
			if err := model.DB.Create(&poolKey).Error; err != nil {
				log.Printf("  [WARN] channel %d: insert key pool failed: %v", ch.Id, err)
				continue
			}
			keysInserted++
		}

		// --- Models → provider_models (skip existing) ---
		for _, m := range ch.GetModels() {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			pm := model.ProviderModel{
				ProfileId:   profile.Id,
				ModelId:     m,
				DisplayName: m,
				Status:      "active",
			}
			result := model.DB.Where("profile_id = ? AND model_id = ?", profile.Id, m).
				Attrs(pm).FirstOrCreate(&pm)
			if result.Error != nil {
				log.Printf("  [WARN] channel %d: upsert model %q failed: %v", ch.Id, m, result.Error)
				continue
			}
			modelsUpserted++
		}

		// --- Store profile_id in channel settings for traceability ---
		writeProfileID(ch, profile.Id)
		if err := model.DB.Model(ch).Update("settings", ch.OtherSettings).Error; err != nil {
			log.Printf("  [WARN] channel %d: update settings failed: %v", ch.Id, err)
		}

		migrated++
		fmt.Printf("  [OK] channel %d (%s, type=%d) → profile %d (%s)\n",
			ch.Id, ch.Name, ch.Type, profile.Id, profile.Name)
	}

	fmt.Printf("\n=== Migration complete ===\n")
	fmt.Printf("  channels migrated: %d\n", migrated)
	fmt.Printf("  channels skipped:  %d\n", skipped)
	fmt.Printf("  keys inserted:     %d\n", keysInserted)
	fmt.Printf("  models upserted:   %d\n", modelsUpserted)
}

// getOrCreateProfile finds a profile by (adapter_type, base_url) or creates one.
func getOrCreateProfile(spec providerSpec, baseURL string) *model.ProviderProfile {
	var p model.ProviderProfile
	err := model.DB.Where("adapter_type = ? AND base_url = ?", spec.adapterType, baseURL).
		First(&p).Error
	if err == nil {
		return &p
	}
	p = model.ProviderProfile{
		Name:        spec.profileName,
		DisplayName: spec.displayName,
		BaseURL:     baseURL,
		AuthMode:    "bearer",
		AdapterType: spec.adapterType,
		Status:      "active",
		Version:     1,
	}
	if err := model.DB.Create(&p).Error; err != nil {
		// ponytail: name 唯一索引冲突时追加 base_url 区分（同一 provider 多 endpoint 场景）。
		p.Name = spec.profileName + "-" + sanitizeURL(baseURL)
		if err := model.DB.Create(&p).Error; err != nil {
			log.Fatalf("create provider profile: %v", err)
		}
	}
	fmt.Printf("  [NEW] profile %q created (adapter=%s, base_url=%s)\n", p.Name, spec.adapterType, baseURL)
	return &p
}

// readProfileID extracts gateway_profile_id from the channel OtherSettings JSON.
func readProfileID(settings string) int {
	if settings == "" {
		return 0
	}
	m := make(map[string]any)
	if err := common.Unmarshal([]byte(settings), &m); err != nil {
		return 0
	}
	v, ok := m["gateway_profile_id"]
	if !ok {
		return 0
	}
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// writeProfileID sets gateway_profile_id in the channel OtherSettings JSON.
func writeProfileID(ch *model.Channel, profileID int) {
	m := make(map[string]any)
	if ch.OtherSettings != "" {
		_ = common.Unmarshal([]byte(ch.OtherSettings), &m)
	}
	m["gateway_profile_id"] = profileID
	if data, err := common.Marshal(m); err == nil {
		ch.OtherSettings = string(data)
	}
}

// sanitizeURL turns a base URL into a safe profile-name suffix.
func sanitizeURL(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	return strings.NewReplacer("/", "-", ".", "-", ":", "-").Replace(u)
}
