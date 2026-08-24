/**
 * 滚动更新期间的静态资源自愈 —— 必须在入口 JS **之前**同步执行。
 *
 * # 为什么需要
 *
 * `maxSurge:1 / maxUnavailable:0` 会让新旧 pod 并存一段时间，此时 Service 有
 * 两个 endpoint，请求轮询分发：
 *
 *   1. GET /index.html            → 落到【新】pod → 返回引用 index-BYovME32.js 的 HTML
 *   2. GET /assets/index-BYovME32.js → 轮询落到【旧】pod → 旧 pod 只有 index-DCVmA3aq.js
 *                                    → nginx try_files $uri =404 → 404
 *   3. 入口 JS 没加载 → React 没挂载 → **纯白页面**
 *
 * 反方向同样成立，每次刷新约 50% 概率白屏。
 * Vite 按内容哈希命名，两版文件名必然不同，所以这个窗口不可能靠缓存策略消除。
 *
 * # 为什么不用 Vite 的 vite:preloadError
 *
 * 那个事件只覆盖**动态 import**。这里挂掉的是入口 JS —— 它还没执行，
 * 任何写在应用代码里的处理都注册不上。只能在 HTML 里内联、提前监听。
 *
 * # ⚠️ 修这个的那一版，自己升级时仍会白屏一次
 *
 * 改动落在 index.html 里，而这一版上线时经历的还是**旧版本的**滚动更新过程。
 * 从下一版开始才有效果。
 */
;(() => {
  var KEY = 'ops.assetReloaded'

  /** 刷过一次还是失败 —— 显示一句人话，最坏情况也不该是纯白页面 */
  function notice() {
    var show = function () {
      if (!document.body || document.getElementById('ops-asset-notice')) return
      var d = document.createElement('div')
      d.id = 'ops-asset-notice'
      d.textContent = '正在更新，请稍后刷新页面 / Updating, please refresh in a moment'
      // 🔴 样式全内联，且**不能用设计令牌**：CSS 很可能和 JS 一起 404 了，
      //    这一层要在"什么样式表都没加载成功"的前提下依然可读。
      // ⚠️ 用 CSS 系统颜色关键字（Canvas / CanvasText / GrayText）而不是写死色值：
      //    它们由浏览器按用户的深浅色偏好给值，既不硬编码也不依赖样式表 ——
      //    这是这个场景下唯一同时满足两个约束的选择。
      d.style.cssText =
        'position:fixed;left:50%;top:40%;transform:translate(-50%,-50%);z-index:2147483647;' +
        'padding:16px 20px;border-radius:8px;font:14px/1.6 system-ui,-apple-system,sans-serif;' +
        'background:Canvas;color:CanvasText;border:1px solid GrayText;text-align:center'
      document.body.appendChild(d)
    }
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', show)
    } else {
      show()
    }
  }

  window.addEventListener(
    'error',
    function (e) {
      var el = e.target
      // 只管资源加载失败（script/link/img），不管运行时异常 —— 后者 target 是 window
      if (!el || el === window) return
      var src = el.src || el.href
      if (typeof src !== 'string' || src.indexOf('/assets/') === -1) return

      // 🔴 只自动刷**一次**。新 pod 也没有那个文件时（真 404、构建产物缺失），
      //    不设上限会变成无限刷新循环 —— 那比白屏更糟，用户连关页面都来不及。
      try {
        if (sessionStorage.getItem(KEY)) {
          notice()
          return
        }
        sessionStorage.setItem(KEY, '1')
      } catch (_) {
        // 隐私模式下 sessionStorage 会抛错。宁可不自愈也不能无限刷。
        notice()
        return
      }
      location.reload()
    },
    // ⚠️ 必须捕获阶段：资源加载错误**不冒泡**，冒泡阶段收不到。
    true,
  )

  // 整页正常加载完 → 清掉计数，下次升级窗口还能再自愈一次
  window.addEventListener('load', function () {
    try {
      sessionStorage.removeItem(KEY)
    } catch (_) {
      /* 忽略：拿不到 sessionStorage 时本来也没写进去 */
    }
  })
})()
