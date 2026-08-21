package logmanager

import (
	"path/filepath"
	"strconv"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
)

const logDBFileName = "log.db"
const dayLayout = "20060102"

var clock = time.Now

func logDBPath() string {
	return filepath.Join(ainit.StaticConfig.LogArchiveDir, logDBFileName)
}

func walDeploymentDir(deploymentID int32) string {
	return apigen.RunOutputDeploymentDir(deploymentID)
}

func archiveDeploymentDir(deploymentID int32) string {
	return filepath.Join(ainit.StaticConfig.LogArchiveDir, strconv.Itoa(int(deploymentID)))
}

func archiveDayDir(deploymentID int32, day int32) string {
	return filepath.Join(archiveDeploymentDir(deploymentID), dayDirName(day))
}

func dayDirName(day int32) string {
	return time.Unix(int64(day)*daySeconds, 0).UTC().Format(dayLayout)
}

func parseDayDirName(name string) (int32, bool) {
	t, err := time.ParseInLocation(dayLayout, name, time.UTC)
	if err != nil {
		return 0, false
	}
	return int32(t.Unix() / daySeconds), true
}
