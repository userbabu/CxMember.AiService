package relay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
)

type Store struct{ db *sql.DB }

func OpenStore(dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("DATABASE_DSN is required")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database DSN: %w", err)
	}
	if cfg.DBName != "cx_ec" {
		return nil, errors.New("DATABASE_DSN must target the existing cx_ec database")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(30)
	db.SetMaxIdleConns(10)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	var unit string
	var scale int64
	if err := db.QueryRow(`SELECT unit,micro_per_credit FROM ai_billing_unit WHERE id=1`).Scan(&unit, &scale); err != nil || unit != "credit" || scale != microPerCredit {
		db.Close()
		return nil, errors.New("credit unit migration is required before starting the relay")
	}
	var modelTypeColumn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='ai_model' AND column_name='model_type'`).Scan(&modelTypeColumn); err != nil {
		db.Close()
		return nil, fmt.Errorf("inspect model type migration: %w", err)
	}
	if modelTypeColumn != 1 {
		db.Close()
		return nil, errors.New("model type migration is required before starting the relay")
	}
	var imageColumn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='ai_model' AND column_name='allow_images' AND is_nullable='NO'`).Scan(&imageColumn); err != nil || imageColumn != 1 {
		db.Close()
		return nil, errors.New("model image input migration is required before starting the relay")
	}
	var imageCountColumn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='ai_model' AND column_name='max_images_per_request' AND is_nullable='NO'`).Scan(&imageCountColumn); err != nil || imageCountColumn != 1 {
		db.Close()
		return nil, errors.New("model image count migration 022 is required")
	}
	var imageLimitColumn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='ai_model' AND column_name='max_image_bytes' AND is_nullable='NO'`).Scan(&imageLimitColumn); err != nil || imageLimitColumn != 1 {
		db.Close()
		return nil, errors.New("model image size migration 021 is required")
	}
	var modelOrderColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='ai_model' AND column_name IN ('sort_order','is_default') AND is_nullable='NO'`).Scan(&modelOrderColumns); err != nil || modelOrderColumns != 2 {
		db.Close()
		return nil, errors.New("model order migration 023 is required")
	}
	var visionPricingColumns, visionSnapshotColumn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='ai_model_pricing' AND column_name IN ('vision_input_price_micro_per_1k','vision_cache_input_price_micro_per_1k','vision_output_price_micro_per_1k','peak_vision_input_price_micro_per_1k','peak_vision_cache_input_price_micro_per_1k','peak_vision_output_price_micro_per_1k','limited_vision_input_price_micro_per_1k','limited_vision_cache_input_price_micro_per_1k','limited_vision_output_price_micro_per_1k') AND is_nullable='NO'`).Scan(&visionPricingColumns); err != nil || visionPricingColumns != 9 {
		db.Close()
		return nil, errors.New("vision token pricing migration is required before starting the relay")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='ai_usage_pricing' AND column_name='billing_input_kind'`).Scan(&visionSnapshotColumn); err != nil || visionSnapshotColumn != 1 {
		db.Close()
		return nil, errors.New("vision pricing snapshot migration is required before starting the relay")
	}
	var generationColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND ((table_name='ai_model' AND column_name IN ('generation_api','generation_size')) OR (table_name='ai_model_pricing' AND column_name IN ('generation_price_micro_per_unit','generation_cost_micro_yuan_per_unit','peak_generation_price_micro_per_unit','peak_generation_cost_micro_yuan_per_unit','limited_generation_price_micro_per_unit','limited_generation_cost_micro_yuan_per_unit')) OR (table_name='ai_usage_pricing' AND column_name IN ('generation_units','generation_unit_price_micro')) OR (table_name='ai_generation' AND column_name='model_id' AND character_maximum_length=120))`).Scan(&generationColumns); err != nil || generationColumns != 11 {
		db.Close()
		return nil, errors.New("media generation migrations 018 and 019 are required")
	}
	var packetLimit int64
	if err := db.QueryRow(`SELECT @@max_allowed_packet`).Scan(&packetLimit); err != nil || packetLimit < 64<<20 {
		db.Close()
		return nil, errors.New("generation results require MySQL max_allowed_packet >= 64 MiB")
	}
	var clientPolicyColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='ai_eder_service_config' AND column_name IN ('blocked_eder_versions','blocked_software_versions')`).Scan(&clientPolicyColumns); err != nil {
		db.Close()
		return nil, fmt.Errorf("inspect Eder client policy migration: %w", err)
	}
	if clientPolicyColumns != 2 {
		db.Close()
		return nil, errors.New("Eder client policy migration is required before starting the relay")
	}
	var globalPromptColumn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='ai_eder_service_config' AND column_name='global_prompt' AND data_type='mediumtext' AND is_nullable='NO'`).Scan(&globalPromptColumn); err != nil {
		db.Close()
		return nil, fmt.Errorf("inspect Eder global prompt migration: %w", err)
	}
	if globalPromptColumn != 1 {
		db.Close()
		return nil, errors.New("Eder global prompt migration is required before starting the relay")
	}
	var defaultsColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='ai_eder_service_config' AND column_name IN ('default_reasoning_effort','default_context_tokens') AND is_nullable='NO'`).Scan(&defaultsColumns); err != nil || defaultsColumns != 2 {
		db.Close()
		return nil, errors.New("Eder chat defaults migration 020 is required")
	}
	var accountDisableColumn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='ai_account' AND column_name='disabled_message'`).Scan(&accountDisableColumn); err != nil {
		db.Close()
		return nil, fmt.Errorf("inspect AI account disable migration: %w", err)
	}
	if accountDisableColumn != 1 {
		db.Close()
		return nil, errors.New("AI account disable migration is required before starting the relay")
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }

type EderServiceConfig struct {
	DefaultReasoningEffort  string
	DefaultContextTokens    int
	Enabled                 bool
	GlobalPrompt            string
	UnavailableMessage      string
	BlockedEderVersions     []string
	BlockedSoftwareVersions []string
}

func (s *Store) ederServiceConfig(ctx context.Context) (EderServiceConfig, error) {
	var config EderServiceConfig
	var enabled int
	var blockedEderVersions, blockedSoftwareVersions []byte
	err := s.db.QueryRowContext(ctx, `SELECT enabled,unavailable_message,blocked_eder_versions,blocked_software_versions,global_prompt,default_reasoning_effort,default_context_tokens FROM ai_eder_service_config WHERE config_id=1`).Scan(&enabled, &config.UnavailableMessage, &blockedEderVersions, &blockedSoftwareVersions, &config.GlobalPrompt, &config.DefaultReasoningEffort, &config.DefaultContextTokens)
	if err != nil {
		return EderServiceConfig{}, err
	}
	if enabled != 0 && enabled != 1 {
		return EderServiceConfig{}, errors.New("Eder service enabled setting is invalid")
	}
	if !utf8.ValidString(config.GlobalPrompt) || utf8.RuneCountInString(config.GlobalPrompt) > 10000 {
		return EderServiceConfig{}, errors.New("Eder global prompt configuration is invalid")
	}
	if err := json.Unmarshal(blockedEderVersions, &config.BlockedEderVersions); err != nil || config.BlockedEderVersions == nil || !validBlockedVersions(config.BlockedEderVersions) {
		return EderServiceConfig{}, errors.New("Eder blocked version configuration is invalid")
	}
	if err := json.Unmarshal(blockedSoftwareVersions, &config.BlockedSoftwareVersions); err != nil || config.BlockedSoftwareVersions == nil || !validBlockedVersions(config.BlockedSoftwareVersions) {
		return EderServiceConfig{}, errors.New("Eder blocked software version configuration is invalid")
	}
	if reasoningRank(config.DefaultReasoningEffort) < 0 || !validContextLimit(config.DefaultContextTokens) {
		return EderServiceConfig{}, errors.New("invalid Eder chat defaults")
	}
	config.Enabled = enabled == 1
	return config, nil
}
