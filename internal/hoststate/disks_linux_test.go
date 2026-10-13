package hoststate

import (
	"strings"
	"testing"
)

// Um /proc/self/mounts de Ubuntu com snaps, Docker, NFS e um caminho com espaço.
const mountsUbuntu = `sysfs /sys sysfs rw,nosuid,nodev,noexec,relatime 0 0
proc /proc proc rw,nosuid,nodev,noexec,relatime 0 0
udev /dev devtmpfs rw,nosuid,relatime,size=4013196k,nr_inodes=1003299,mode=755 0 0
tmpfs /run tmpfs rw,nosuid,nodev,noexec,relatime,size=809096k,mode=755 0 0
/dev/sda2 / ext4 rw,relatime 0 0
/dev/loop3 /snap/core22/1380 squashfs ro,nodev,relatime,errors=continue 0 0
/dev/sda1 /boot/efi vfat rw,relatime,fmask=0077,dmask=0077 0 0
/dev/sdb1 /srv/dados\040novos xfs rw,relatime 0 0
overlay /var/lib/docker/overlay2/abc/merged overlay rw,relatime,lowerdir=/x 0 0
nas:/export /mnt/nas nfs4 rw,relatime,vers=4.2 0 0
//fs/publico /mnt/publico cifs rw,relatime 0 0
/dev/sr0 /media/cdrom iso9660 ro,nosuid,nodev 0 0
/dev/sda2 /var/snap/lxd/common/ns ext4 rw,relatime 0 0
`

func TestParseMounts(t *testing.T) {
	got, err := parseMounts(strings.NewReader(mountsUbuntu))
	if err != nil {
		t.Fatal(err)
	}
	quer := []string{"/ ext4 /dev/sda2", "/boot/efi vfat /dev/sda1", "/srv/dados novos xfs /dev/sdb1", "/var/snap/lxd/common/ns ext4 /dev/sda2"}
	if len(got) != len(quer) {
		t.Fatalf("montagens: %+v", got)
	}
	for i, d := range got {
		if s := d.Mount + " " + d.FS + " " + d.Device; s != quer[i] {
			t.Errorf("%d: %q, quer %q", i, s, quer[i])
		}
	}
	// O mesmo dispositivo em dois lugares vira um volume só, o de caminho mais curto.
	if d := dedupe(got); len(d) != 3 || d[0].Mount != "/" {
		t.Errorf("dedupe: %+v", d)
	}
}

func TestUnescapeMount(t *testing.T) {
	for in, quer := range map[string]string{`/a\040b`: "/a b", `/x\011y`: "/x\ty", `/c\134d`: `/c\d`, `/fim\04`: `/fim\04`, "/sem": "/sem"} {
		if got := unescapeMount(in); got != quer {
			t.Errorf("%q: %q, quer %q", in, got, quer)
		}
	}
}
