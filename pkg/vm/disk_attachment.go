// SPDX-License-Identifier: MIT

package vm

// DiskAttachments is a collection of disk attachments, as embedded in a VM by
// GET /vms?follow=disk_attachments
type DiskAttachments struct {
	Attachment []DiskAttachment `xml:"disk_attachment"`
}

// DiskAttachment represents the disk attachment resource. Only the disk id is
// present here; the disk itself comes from the /disks collection.
type DiskAttachment struct {
	ID          string `xml:"id,attr"`
	LogicalName string `xml:"logical_name"`
	Bootable    bool   `xml:"bootable"`
	Disk        struct {
		ID string `xml:"id,attr"`
	} `xml:"disk"`
}
