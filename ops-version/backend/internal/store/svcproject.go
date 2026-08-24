package store

import (
	"context"
	"strings"
)

// ProjectOfService 推断某个服务在**我方 Harbor** 的哪个项目下。
//
// 🔴 存在的理由：「和我方 Harbor 逐版本比对」需要项目，而原来是**让人选**的 ——
//
//	一个 Harbor 有十几个项目、每个几百个仓库，人根本不知道自己要查的服务在哪个下面，
//	选错了下拉里就没有它（用户实测卡在这一步）。
//
// 而这件事**根本不用问**：我方平台的快照里存着 image_repo，形如 `appA/wallet-backend`，
// 冒号前那段就是项目名。
//
// ⚠️ 只从**我方**（is_self）平台推：别家平台的 image_repo 指向的是他们自己的 Harbor，
// 拿它去查我方 Harbor 会查一个不存在的项目，然后得到「这个服务没有任何版本」——
// 而那跟「服务名打错了」在界面上分不出来。
func (s *Store) ProjectOfService(ctx context.Context, serviceKey string) (string, error) {
	var repo string
	err := s.db.QueryRowContext(ctx, `
		SELECT sv.image_repo
		  FROM service_versions sv
		  JOIN orgs o ON o.id = sv.org_id
		 WHERE sv.service_key = ? AND o.is_self = 1 AND o.deleted_at IS NULL
		   AND sv.image_repo <> ''
		 ORDER BY sv.observed_at DESC LIMIT 1`, serviceKey).Scan(&repo)
	if err != nil {
		return "", err
	}
	// `appA/wallet-backend` → `appA`；没有斜杠说明它在 Harbor 的根项目（library）
	if i := strings.LastIndex(repo, "/"); i > 0 {
		return repo[:i], nil
	}
	return "library", nil
}

// HarborOfService 推断该拿哪个 Harbor 去查这个服务的版本。
//
// 🔴 和 ProjectOfService 同一个道理：不该让人选。
//
//	深入比对是从**推送记录**那一行点进去的，而那条记录本来就属于某条复制规则，
//	规则又挂在某个 Harbor 上 —— 答案在库里，问人反而是把已知的东西再要一遍。
//
// ⚠️ 界面上常选「全部规则」，那时前端手上**根本没有 harbor_id**。
//
//	逼它先选一个 Harbor 就等于把刚去掉的前置步骤又加回来了。
//
// 返回 0 表示推不出来 —— 调用方要据此报「推不出来」，
// 不能默默拿第一个 Harbor 去查：查出来的「没有这个版本」会被当成事实。
func (s *Store) HarborOfService(ctx context.Context, serviceKey string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT p.harbor_id
		  FROM sync_tasks t
		  JOIN sync_policies p ON p.id = t.policy_ref
		 WHERE t.service_key = ?
		 ORDER BY t.finished_at DESC, t.id DESC LIMIT 1`, serviceKey).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}
