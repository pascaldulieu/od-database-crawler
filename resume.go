package main

import (
	"encoding/gob"
	"github.com/beeker1121/goque"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func LoadResumeTasks(inRemotes chan<- *OD) {
	resumed, err := ResumeTasks()
	if err != nil {
		logrus.WithError(err).
			Error("Failed to resume queued tasks. " +
				"/queue is probably corrupt")
		err = nil
	}

	for _, remote := range resumed {
		inRemotes <- remote
	}
}

func ResumeTasks() (tasks []*OD, err error) {
	// Get files in /queue
	var queueF *os.File
	var entries []os.FileInfo
	queueF, err = os.Open("queue")
	if err != nil { return nil, err }
	defer queueF.Close()
	entries, err = queueF.Readdir(-1)
	if err != nil { return nil, err }

	resumeDur := viper.GetDuration(ConfResume)

	for _, entry := range entries {
		if !entry.IsDir() { continue }

		// Check if name is a number
		var id uint64
		if id, err = strconv.ParseUint(entry.Name(), 10, 64); err != nil {
			continue
		}

		// Too old to be resumed
		timeDelta := time.Since(entry.ModTime())
		if resumeDur >= 0 && timeDelta > resumeDur {
			removeOldQueue(id)
			continue
		}

		// Load queue
		var od *OD
		if od, err = resumeQueue(id); err != nil {
			logrus.WithError(err).
				WithField("id", id).
				Warning("Failed to load paused task")
			continue
		} else if od == nil {
			removeOldQueue(id)
			continue
		}

		tasks = append(tasks, od)
	}

	return tasks, nil
}

func resumeQueue(id uint64) (od *OD, err error) {
	fPath := filepath.Join("queue", strconv.FormatUint(id, 10))

	// Try to find pause file
	pausedF, err := os.Open(filepath.Join(fPath, "PAUSED"))
	if os.IsNotExist(err) {
		// No PAUSED file => not paused
		// not paused => no error
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	defer pausedF.Close()

	od = new(OD)
	od.WCtx.OD = od

	// Make the paused struct point to OD fields
	// So gob loads values into the OD struct
	paused := PausedOD {
		Task:    &od.Task,
		Result:  &od.Result,
		BaseUri: &od.BaseUri,
	}

	// Read pause settings
	pauseDec := gob.NewDecoder(pausedF)
	err = pauseDec.Decode(&paused)
	if err != nil { return nil, err }

	// Read pause scan state
	err = od.Scanned.Unmarshal(pausedF)
	if err != nil { return nil, err }

	// Open queue
	bq, err := OpenQueue(fPath)
	if err != nil { return nil, err }

	od.WCtx.Queue = bq

	return od, nil
}

func removeOldQueue(id uint64) {
	if id == 0 {
		// TODO Make custom crawl less of an ugly hack
		return
	}

	logrus.WithField("id", id).
		Warning("Deleting & returning old task")

	name := strconv.FormatUint(id, 10)

	fPath := filepath.Join("queue", name)

	// Acquire old queue
	q, err := goque.OpenQueue(fPath)
	if err != nil {
		// Queue lock exists, don't delete
		logrus.WithField("err", err).
			WithField("path", fPath).
			Error("Failed to acquire old task")
		return
	}

	// Delete old queue from disk
	err = q.Drop()
	if err != nil {
		// Queue lock exists, don't delete
		logrus.WithField("err", err).
			WithField("path", fPath).
			Error("Failed to delete old task")
		return
	}

	// Delete old crawl result from disk
	_ = os.Remove(filepath.Join("crawled", name + ".json"))

	// Return task to server
	if err := CancelTask(id); err != nil {
		// Queue lock exists, don't delete
		logrus.WithField("err", err).
			WithField("id", id).
			Warning("Failed to return unfinished task to server")
		return
	}
}
