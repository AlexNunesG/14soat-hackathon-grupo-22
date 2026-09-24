package integration

// Integration tests for RF5, "on error, notify the user" (docs/openapi.yaml,
// "Failure notification (RF5)"): a FAILED video sends its owner an e-mail
// whose subject contains the original file name and whose body contains the
// error_message; a DONE video sends none. E-mails are read from MailHog
// (MAILHOG_URL); each test only reads the mailbox of its own unique user.

import (
	"testing"
	"time"
)

// noMailGrace is how long the tests wait, after a video's status is final,
// before concluding that no further e-mail is coming. Notification is
// asynchronous, so absence can only be checked after some wait: in the
// stack a failure e-mail arrives well within a second, so 5 s makes an
// unwanted e-mail very likely to be seen without slowing the suite much.
const noMailGrace = 5 * time.Second

// TestFailedVideoNotifiesOwner uploads a good and a corrupt video; the owner
// gets exactly one e-mail, about the corrupt one. The name has non-ASCII
// letters, so the subject must survive MIME encoding.
func TestFailedVideoNotifiesOwner(t *testing.T) {
	t.Parallel()
	user, token := registerAndLogin(t)
	uploaded := mustUpload(t, token,
		namedFile{"boas férias.mp4", makeMP4(t, 1)},
		namedFile{"férias corrompidas.mp4", corruptVideo()},
	)
	waitForStatus(t, token, uploaded[0].ID, statusDone)
	failed := waitForStatus(t, token, uploaded[1].ID, statusFailed)

	waitForMails(t, user.Email, 1)
	// Wait for late duplicates or an e-mail about the DONE video.
	time.Sleep(noMailGrace)
	mails := mailsFor(t, user.Email)

	if len(mails) != 1 {
		t.Fatalf("%s received %d e-mails, want exactly 1 (for the FAILED video); subjects: %q", user.Email, len(mails), subjects(mails))
	}
	assertFailureMail(t, mails[0], user.Email, failed)
}

// TestDoneVideosSendNoFailureMail: a user whose videos all end DONE gets no
// e-mail, checked noMailGrace after the last one is DONE.
func TestDoneVideosSendNoFailureMail(t *testing.T) {
	t.Parallel()
	user, token := registerAndLogin(t)
	uploaded := mustUpload(t, token,
		namedFile{"first.mp4", makeMP4(t, 1)},
		namedFile{"second.mkv", makeVideo(t, "mkv", "libx264", 2)},
	)
	for _, v := range uploaded {
		waitForStatus(t, token, v.ID, statusDone)
	}

	time.Sleep(noMailGrace)

	if mails := mailsFor(t, user.Email); len(mails) != 0 {
		t.Errorf("%s received %d e-mails although every video ended DONE; subjects: %q", user.Email, len(mails), subjects(mails))
	}
}
