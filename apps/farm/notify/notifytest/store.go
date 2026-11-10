package notifytest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

var t0 = time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)

func person(id string, farms ...string) notify.Recipient {
	return notify.Recipient{ID: id, Name: "Ravi " + id, Phone: "+9198765432" + id[len(id)-2:], Farms: farms, CreatedAt: t0, UpdatedAt: t0}
}

// RunStore runs the suite on fresh stores from open.
func RunStore(t *testing.T, open func(t *testing.T) notify.Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("recipients", func(t *testing.T) {
		s := open(t)
		a, b := person("r01", "f1"), person("r02")
		a.Email, a.Telegram, a.LinkCode = "Ravi@Example.com", "555", "code12345"
		for _, r := range []notify.Recipient{b, a} {
			if err := s.PutRecipient(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.PutRecipient(ctx, notify.Recipient{ID: "bad", Phone: "98765"}); !errors.Is(err, notify.ErrInvalid) {
			t.Errorf("a bad phone: %v, want ErrInvalid", err)
		}
		all, err := s.ListRecipients(ctx)
		if err != nil || len(all) != 2 || all[0].ID != "r01" {
			t.Fatalf("List = %+v, %v", all, err)
		}
		a.Name, a.CreatedAt = "Ravi Kumar", t0.Add(time.Hour)
		if err := s.PutRecipient(ctx, a); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRecipient(ctx, "r01")
		if err != nil || got.Name != "Ravi Kumar" || !got.CreatedAt.Equal(t0) {
			t.Errorf("Get after a replace = %+v, %v; want the new name and the first CreatedAt", got, err)
		}
		for farm, want := range map[string][]string{"f1": {"r01", "r02"}, "f2": {"r02"}} {
			rs, err := s.RecipientsFor(ctx, farm)
			if err != nil || len(rs) != len(want) {
				t.Errorf("RecipientsFor(%s) = %d, %v; want %v", farm, len(rs), err, want)
				continue
			}
			for i := range want {
				if rs[i].ID != want[i] {
					t.Errorf("RecipientsFor(%s)[%d] = %s, want %s", farm, i, rs[i].ID, want[i])
				}
			}
		}
		for k, addr := range map[notify.AddressKind]string{notify.AddrPhone: a.Phone, notify.AddrEmail: "ravi@example.COM", notify.AddrTelegram: "555", "link": "code12345"} {
			if r, err := s.FindRecipient(ctx, k, addr); err != nil || r.ID != "r01" {
				t.Errorf("Find(%s, %s) = %s, %v; want r01", k, addr, r.ID, err)
			}
		}
		if _, err := s.FindRecipient(ctx, notify.AddrTelegram, "nobody"); !errors.Is(err, notify.ErrNotFound) {
			t.Errorf("Find of nobody: %v, want ErrNotFound", err)
		}
		if _, err := s.FindRecipient(ctx, notify.AddrEmail, ""); !errors.Is(err, notify.ErrNotFound) {
			t.Errorf("Find of an empty address: %v, want ErrNotFound", err)
		}
		if err := s.DeleteRecipient(ctx, "r02"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetRecipient(ctx, "r02"); !errors.Is(err, notify.ErrNotFound) {
			t.Errorf("Get of a deleted one: %v", err)
		}
		if err := s.DeleteRecipient(ctx, "r02"); !errors.Is(err, notify.ErrNotFound) {
			t.Errorf("Delete twice: %v", err)
		}
	})

	t.Run("update is atomic", func(t *testing.T) {
		s := open(t)
		if err := s.PutRecipient(ctx, person("r01")); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := range 20 {
			wg.Go(func() {
				_, err := s.Update(ctx, "r01", func(r *notify.Recipient) error {
					if r.Consent == nil {
						r.Consent = map[string]notify.Consent{}
					}
					r.Consent[fmt.Sprintf("lane%d", i)] = notify.Consent{OptedIn: true, At: t0}
					return nil
				})
				if err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		r, _ := s.GetRecipient(ctx, "r01")
		if len(r.Consent) != 20 {
			t.Errorf("after 20 updates at once, %d consents: updates were lost", len(r.Consent))
		}
		boom := errors.New("boom")
		if _, err := s.Update(ctx, "r01", func(r *notify.Recipient) error { r.Name = "x"; return boom }); !errors.Is(err, boom) {
			t.Errorf("Update's own error: %v", err)
		}
		if r, _ := s.GetRecipient(ctx, "r01"); r.Name == "x" {
			t.Error("a failed update was saved")
		}
		if _, err := s.Update(ctx, "r01", func(r *notify.Recipient) error { r.Phone = "123"; return nil }); !errors.Is(err, notify.ErrInvalid) {
			t.Errorf("an update to a bad phone: %v, want ErrInvalid", err)
		}
		if _, err := s.Update(ctx, "nobody", func(*notify.Recipient) error { return nil }); !errors.Is(err, notify.ErrNotFound) {
			t.Errorf("Update of nobody: %v", err)
		}
	})

	t.Run("the outbox", func(t *testing.T) {
		s := open(t)
		m := notify.Message{ID: "n1:r01", To: "r01", Notice: "n1", Kind: notify.KindAlert, Severity: notify.Urgent, Title: "Rain", CreatedAt: t0}
		if ok, err := s.Enqueue(ctx, m, t0); err != nil || !ok {
			t.Fatalf("Enqueue = %v, %v", ok, err)
		}
		if ok, err := s.Enqueue(ctx, m, t0); err != nil || ok {
			t.Errorf("Enqueue twice = %v, %v; want false", ok, err)
		}
		later := m
		later.ID, later.Notice, later.CreatedAt = "n2:r01", "n2", t0.Add(time.Second)
		if _, err := s.Enqueue(ctx, later, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		items, err := s.Claim(ctx, t0, time.Minute, 10)
		if err != nil || len(items) != 1 || items[0].Message.ID != "n1:r01" || items[0].Message.Title != "Rain" {
			t.Fatalf("Claim at t0 = %+v, %v; want n1 only", items, err)
		}
		if items, _ := s.Claim(ctx, t0.Add(30*time.Second), time.Minute, 10); len(items) != 0 {
			t.Errorf("Claim inside the lease = %d items, want 0", len(items))
		}
		if items, _ := s.Claim(ctx, t0.Add(61*time.Second), time.Minute, 10); len(items) != 1 {
			t.Errorf("Claim after the lease = %d items, want 1 (a crashed worker's item comes back)", len(items))
		}
		if err := s.Reschedule(ctx, "n1:r01", t0.Add(10*time.Minute), 2, "rate limited"); err != nil {
			t.Fatal(err)
		}
		it, err := s.GetItem(ctx, "n1:r01")
		if err != nil || it.Attempts != 2 || it.LastError != "rate limited" || !it.NextAt.Equal(t0.Add(10*time.Minute)) || it.Status != notify.ItemPending {
			t.Errorf("after Reschedule = %+v, %v", it, err)
		}
		if err := s.Finish(ctx, "n1:r01", notify.ItemDone, 3, "", t0.Add(11*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if items, _ := s.Claim(ctx, t0.Add(24*time.Hour), time.Minute, 10); len(items) != 1 || items[0].Message.ID != "n2:r01" {
			t.Errorf("Claim after Finish = %+v: want only n2", items)
		}
		if err := s.Reschedule(ctx, "n1:r01", t0, 1, ""); !errors.Is(err, notify.ErrNotFound) {
			t.Errorf("Reschedule of a finished item: %v, want ErrNotFound", err)
		}
		list, err := s.ListItems(ctx, notify.ItemFilter{Recipient: "r01"})
		if err != nil || len(list) != 2 || list[0].Message.ID != "n2:r01" {
			t.Errorf("ListItems = %+v, %v; want newest first", list, err)
		}
		if list, _ := s.ListItems(ctx, notify.ItemFilter{Notice: "n1"}); len(list) != 1 || list[0].Status != notify.ItemDone {
			t.Errorf("ListItems by notice = %+v", list)
		}
		if err := s.Prune(ctx, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetItem(ctx, "n1:r01"); !errors.Is(err, notify.ErrNotFound) {
			t.Errorf("a finished item older than the prune time: %v, want gone", err)
		}
		if _, err := s.GetItem(ctx, "n2:r01"); err != nil {
			t.Errorf("a pending item was pruned: %v", err)
		}
	})

	t.Run("an item is claimed by one worker", func(t *testing.T) {
		s := open(t)
		for i := range 30 {
			m := notify.Message{ID: fmt.Sprintf("m%02d", i), To: "r01", Kind: notify.KindAlert, Severity: notify.Advice, Title: "x", CreatedAt: t0}
			if _, err := s.Enqueue(ctx, m, t0); err != nil {
				t.Fatal(err)
			}
		}
		var got atomic.Int64
		seen := sync.Map{}
		var wg sync.WaitGroup
		for range 6 {
			wg.Go(func() {
				for {
					items, err := s.Claim(ctx, t0, time.Minute, 4)
					if err != nil {
						t.Error(err)
						return
					}
					if len(items) == 0 {
						return
					}
					for _, it := range items {
						if _, dup := seen.LoadOrStore(it.Message.ID, true); dup {
							t.Errorf("%s claimed twice", it.Message.ID)
						}
						got.Add(1)
					}
				}
			})
		}
		wg.Wait()
		if got.Load() != 30 {
			t.Errorf("claimed %d of 30", got.Load())
		}
	})

	t.Run("a delivery is claimed once", func(t *testing.T) {
		s := open(t)
		d := notify.Delivery{Message: "m1", Lane: "sms", Recipient: "r01", Cost: 25, CreatedAt: t0, UpdatedAt: t0}
		var wins atomic.Int64
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				_, ok, err := s.BeginDelivery(ctx, d)
				if err != nil {
					t.Error(err)
				}
				if ok {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("%d workers claimed one delivery, want 1", wins.Load())
		}
		prev, ok, err := s.BeginDelivery(ctx, d)
		if err != nil || ok || prev.Status != notify.StatusSending {
			t.Errorf("Begin while sending = %+v, %v, %v; want the sending one, false", prev, ok, err)
		}
		d.Status, d.Error, d.Retryable, d.UpdatedAt = notify.StatusFailed, "503", true, t0.Add(time.Second)
		if err := s.FinishDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.BeginDelivery(ctx, d); !ok {
			t.Error("a retryable failure was not claimed again")
		}
		d.Status, d.ProviderID, d.Error, d.Retryable = notify.StatusSent, "SM1", "", false
		if err := s.FinishDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
		if prev, ok, _ := s.BeginDelivery(ctx, d); ok || prev.ProviderID != "SM1" {
			t.Errorf("Begin after sent = %+v, %v; want the sent one, false", prev, ok)
		}
		final := notify.Delivery{Message: "m2", Lane: "sms", Recipient: "r01", CreatedAt: t0, UpdatedAt: t0}
		_, _, _ = s.BeginDelivery(ctx, final)
		final.Status, final.Error = notify.StatusFailed, "bad number"
		_ = s.FinishDelivery(ctx, final)
		if _, ok, _ := s.BeginDelivery(ctx, final); ok {
			t.Error("a final failure was claimed again")
		}
		if err := s.FinishDelivery(ctx, notify.Delivery{Message: "none", Lane: "sms"}); !errors.Is(err, notify.ErrNotFound) {
			t.Errorf("Finish of an unclaimed delivery: %v", err)
		}
	})

	t.Run("status only moves forward", func(t *testing.T) {
		s := open(t)
		d := notify.Delivery{Message: "m1", Lane: "whatsapp", Recipient: "r01", CreatedAt: t0, UpdatedAt: t0}
		_, _, _ = s.BeginDelivery(ctx, d)
		d.Status, d.ProviderID = notify.StatusSent, "wamid.1"
		_ = s.FinishDelivery(ctx, d)
		for _, c := range []struct {
			status string
			want   bool
		}{{notify.StatusRead, true}, {notify.StatusDelivered, false}, {notify.StatusSent, false}} {
			ok, err := s.SetStatus(ctx, "whatsapp", "wamid.1", c.status, "", t0.Add(time.Minute))
			if err != nil || ok != c.want {
				t.Errorf("SetStatus(%s) = %v, %v; want %v", c.status, ok, err, c.want)
			}
		}
		if ok, _ := s.SetStatus(ctx, "sms", "wamid.1", notify.StatusFailed, "", t0); ok {
			t.Error("SetStatus matched another lane's ID")
		}
		ds, err := s.ListDeliveries(ctx, notify.DeliveryFilter{Message: "m1"})
		if err != nil || len(ds) != 1 || ds[0].Status != notify.StatusRead || ds[0].ProviderID != "wamid.1" {
			t.Errorf("ListDeliveries = %+v, %v", ds, err)
		}
	})

	t.Run("spend counts what a lane took", func(t *testing.T) {
		s := open(t)
		add := func(msg, lane, status string, cost int64, at time.Time) {
			d := notify.Delivery{Message: msg, Lane: lane, Recipient: "r01", Cost: cost, CreatedAt: at, UpdatedAt: at}
			if _, _, err := s.BeginDelivery(ctx, d); err != nil {
				t.Fatal(err)
			}
			d.Status = status
			if err := s.FinishDelivery(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
		add("a", "sms", notify.StatusSent, 25, t0)
		add("b", "sms", notify.StatusDelivered, 25, t0.Add(time.Hour))
		add("c", "sms", notify.StatusFailed, 0, t0.Add(time.Hour))
		add("d", "sms", notify.StatusSent, 25, t0.Add(-48*time.Hour))
		add("e", "whatsapp", notify.StatusSent, 14, t0)
		n, cost, err := s.Spend(ctx, "sms", t0)
		if err != nil || n != 2 || cost != 50 {
			t.Errorf("Spend(sms, t0) = %d, %d, %v; want 2, 50", n, cost, err)
		}
	})
}
