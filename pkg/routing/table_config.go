package routing

import (
	"os"
	"strconv"
)

// NewTableConfigFromEnv creates a TableConfig from environment variables
func NewTableConfigFromEnv() TableConfig {
	config := DefaultTableConfig()

	// Check for incremental updates flag
	if val := os.Getenv("KEDA_HTTP_ROUTING_USE_INCREMENTAL_UPDATES"); val != "" {
		if useIncremental, err := strconv.ParseBool(val); err == nil {
			config.UseIncrementalUpdates = useIncremental
		}
	}

	// Check for channel size configuration
	if val := os.Getenv("KEDA_HTTP_ROUTING_UPDATE_CHANNEL_SIZE"); val != "" {
		if channelSize, err := strconv.Atoi(val); err == nil && channelSize > 0 {
			config.UpdateChannelSize = channelSize
		}
	}

	// Check for mock objects configuration
	if enableMocks := os.Getenv("KEDA_HTTP_ROUTING_ENABLE_MOCK_OBJECTS"); enableMocks == "true" {
		config.EnableMockObjects = true

		// Check for mock object count
		if mockCount := os.Getenv("KEDA_HTTP_ROUTING_MOCK_OBJECTS_COUNT"); mockCount != "" {
			if count, err := strconv.Atoi(mockCount); err == nil && count > 0 {
				config.MockObjectsCount = count
			}
		}
	}

	return config
}

// Configuration presets for common scenarios
func LegacyTableConfig() TableConfig {
	return TableConfig{
		UseIncrementalUpdates: false,
		UpdateChannelSize:     0, // Not used in legacy mode
	}
}

func IncrementalTableConfig() TableConfig {
	return TableConfig{
		UseIncrementalUpdates: true,
		UpdateChannelSize:     1000,
	}
}

func HighVolumeIncrementalConfig() TableConfig {
	return TableConfig{
		UseIncrementalUpdates: true,
		UpdateChannelSize:     5000, // For very high volume scenarios
	}
}
