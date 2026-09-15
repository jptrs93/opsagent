package systemconfig

import "github.com/jptrs93/opsagent/backend/apigen"

type AssetStorageTarget int

const (
	AssetStorageLocal AssetStorageTarget = iota
	AssetStorageS3
	AssetStorageBoth
)

func (t AssetStorageTarget) UsesS3() bool    { return t != AssetStorageLocal }
func (t AssetStorageTarget) UsesLocal() bool { return t != AssetStorageS3 }

func (t AssetStorageTarget) String() string {
	switch t {
	case AssetStorageS3:
		return "s3"
	case AssetStorageBoth:
		return "both"
	}
	return "local"
}

func LargeAssetStorageTarget(loader Loader, settings apigen.ClusterSettings) AssetStorageTarget {
	if !loader.MustLoadBoolSetting(settings.Backup.Enabled) {
		return AssetStorageLocal
	}
	if loader.MustLoadBoolSetting(settings.LargeAssets.KeepLocalCopy) {
		return AssetStorageBoth
	}
	return AssetStorageS3
}

func (s *Service) largeAssetStorageTarget(settings apigen.ClusterSettings) (AssetStorageTarget, error) {
	backupEnabled, err := s.LoadBoolSetting(settings.Backup.Enabled)
	if err != nil {
		return AssetStorageLocal, err
	}
	if !backupEnabled {
		return AssetStorageLocal, nil
	}
	keepLocal, err := s.LoadBoolSetting(settings.LargeAssets.KeepLocalCopy)
	if err != nil {
		return AssetStorageLocal, err
	}
	if keepLocal {
		return AssetStorageBoth, nil
	}
	return AssetStorageS3, nil
}
