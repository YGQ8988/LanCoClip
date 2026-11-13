(function(){
  var listEl = document.getElementById('list');
  var emptyEl = document.getElementById('empty');
  var countEl = document.getElementById('count');
  var shareUrlEl = document.getElementById('shareUrl');
  var shareEl = document.querySelector('.share');
  var clearBtn = document.getElementById('clearBtn');
  var unfavAllBtn = document.getElementById('unfavAllBtn');
  var settingsBtn = document.getElementById('settingsBtn');
  var confirmModal = document.getElementById('confirmModal');
  var confirmTitleEl = document.getElementById('confirmTitle');
  var confirmTextEl = document.getElementById('confirmText');
  var confirmOkBtn = document.getElementById('confirmOk');
  var confirmCancelBtn = document.getElementById('confirmCancel');
  var modal = document.getElementById('qrModal');
  var qrImg = document.getElementById('qrImg');
  var qrUrl = document.getElementById('qrUrl');
  var settingsVerEl = document.getElementById('settingsVer');
  var toastEl = document.getElementById('toast');
  var pagerEl = document.getElementById('pager');
  var pagesEl = document.getElementById('pages');
  var prevBtn = document.getElementById('prevPage');
  var nextBtn = document.getElementById('nextPage');
  var loadmoreEl = document.getElementById('loadmore');
  var tabText = document.getElementById('tabText');
  var tabImage = document.getElementById('tabImage');
  var tabFavorite = document.getElementById('tabFavorite');
  var badgeText = document.getElementById('badgeText');
  var badgeImage = document.getElementById('badgeImage');
  var badgeFavorite = document.getElementById('badgeFavorite');
  var themeBtn = document.getElementById('themeBtn');
  var settingsModal = document.getElementById('settingsModal');
  var settingsClose = document.getElementById('settingsClose');
  var settingsCancel = document.getElementById('settingsCancel');
  var settingsSave = document.getElementById('settingsSave');
  var maxTextInput = document.getElementById('maxTextInput');
  var maxImageInput = document.getElementById('maxImageInput');
  var setTabGeneral = document.getElementById('setTabGeneral');
  var setTabHotkey = document.getElementById('setTabHotkey');
  var setPaneGeneral = document.getElementById('setPaneGeneral');
  var setPaneHotkey = document.getElementById('setPaneHotkey');
  var autostartField = document.getElementById('autostartField');
  var autostartInput = document.getElementById('autostartInput');
  var pausedField = document.getElementById('pausedField');
  var pausedInput = document.getElementById('pausedInput');
  var statusNotice = document.getElementById('statusNotice');
  var hkQR = document.getElementById('hkQR');
  var hkHistory = document.getElementById('hkHistory');
  var hkQRBtn = document.getElementById('hkQRBtn');
  var hkHistBtn = document.getElementById('hkHistBtn');
  var hotkeySave = document.getElementById('hotkeySave');
  var hotkeyCancel = document.getElementById('hotkeyCancel');
  var historyModal = document.getElementById('historyModal');
  var historyClose = document.getElementById('historyClose');
  var histList = document.getElementById('histList');
  var histEmpty = document.getElementById('histEmpty');
  var addBtn = document.getElementById('addBtn');
  var imagePicker = document.getElementById('imagePicker');
  var addTextModal = document.getElementById('addTextModal');
  var addTextInput = document.getElementById('addTextInput');
  var addTextSubmit = document.getElementById('addTextSubmit');
  var addTextCancel = document.getElementById('addTextCancel');
  var addTextClose = document.getElementById('addTextClose');

  var EMPTY_TEXT = {
    text: '暂无文本记录，去电脑上复制点内容试试',
    image: '暂无图片记录，去电脑上复制张图片试试',
    favorite: '还没有收藏的文本，去「文本」里点一下「收藏」就会出现在这里'
  };

  // 每页条数：文本 / 收藏 28 条 = 4 列 × 7 行，图片 8 条 = 4 列 × 2 行。
  // 服务端只认 size 参数，分页大小完全由前端决定。
  var PAGE_SIZE = { text: 28, image: 8, favorite: 28 };
  var FAB_POS_KEY = 'lanoclip-fab-pos'; // 悬浮「添加」按钮拖动后的位置（视口 px）

  var state = {
    shareUrl:'', canClear:false, openIds:{},
    settings:{maxText:500, maxImage:200, autostart:false, autostartSupported:false, paused:false},
    storageError:'',  // 最近一次写盘异常，变化时只提示一次
    type:'text',      // 当前 tab：text | image | favorite（各自分页）
    page:1,           // PC 当前页
    pages:1,          // H5 已加载页数
    total:0, totalPages:1, hasMore:false,
    favorites:0,      // 收藏总数（tab 角标）
    firstSig:'',      // 第 1 页签名，用于轮询判断是否有新数据
    sig:'',           // 当前已渲染列表的签名
    loading:false,
  };

  function pageSize(){ return PAGE_SIZE[state.type] || PAGE_SIZE.text; }
  function countLabel(){
    return state.type === 'favorite'
      ? '共 ' + state.total + ' 条收藏'
      : '共 ' + state.total + ' 条记录';
  }

  // 手机端一律不展示「清空全部」并默认单列。UA 是第一判据，触摸能力只在
  // 「非桌面系统」时作为补充判据：iPadOS 13+ 的 Safari 会把自己伪装成
  // Macintosh，只能靠多点触控（真 Mac 为 0）识别；而触屏 Windows 笔记本 /
  // 一体机（Surface 等）虽然 maxTouchPoints > 0、pointer 也是 coarse，仍是
  // 本机桌面浏览器——早先只看触摸能力会把它误判成手机，导致清空按钮被移出
  // DOM、托盘「设置」的 ?settings=1 也不弹窗。
  var UA = navigator.userAgent;
  var coarse = !!(window.matchMedia && window.matchMedia('(pointer: coarse)').matches);
  var touchy = (navigator.maxTouchPoints || 0) > 0 || coarse || 'ontouchstart' in window;
  var mobileUA = /Android|iPhone|iPod|iPad|Mobile|Windows Phone|MicroMessenger|HarmonyOS/i.test(UA);
  // iPadOS 13+：UA 写 Macintosh，但有多点触控（真 Mac 为 0，iPad 通常 5）
  var iPadLikeMac = /Macintosh/i.test(UA) && (navigator.maxTouchPoints || 0) > 1;
  // 明确的桌面系统不受触摸屏影响（Windows 平板拆键盘后仍按桌面处理）
  var desktopOS = /Windows NT|X11|CrOS/i.test(UA);
  var isMobile = mobileUA || iPadLikeMac || (touchy && !desktopOS);

  // PC 用页码分页 + 宽屏多列，H5 用上滑加载更多 + 单列
  var mode = isMobile ? 'h5' : 'pc';
  // 托盘「设置」会以 ?settings=1 打开本页并自动弹出设置弹窗；
  // 快捷键分别以 ?qr=1 / ?history=1 打开二维码弹窗 / 历史记录浮层
  var autoOpenSettings = /[?&]settings=1/.test(location.search);
  var autoOpenQR = /[?&]qr=1/.test(location.search);
  var autoOpenHistory = /[?&]history=1/.test(location.search);
  document.body.classList.add(mode === 'pc' ? 'is-pc' : 'is-h5');
  // 图片 tab 固定高度按模式取值（PC 200px / H5 240px），直接读 CSS 变量保证判断与样式一致；
  // 必须在 body 加上 is-pc/is-h5 之后再读，否则拿到的还是 :root 的 PC 值
  var SHOT_H = parseFloat(getComputedStyle(document.body).getPropertyValue('--shot-h')) || 200;
  // PC 每页按类型定（文本/收藏 28 = 4 列 × 7 行，图片 8 = 4 列 × 2 行），
  // 手机上同样按这个粒度上滑追加
  if (mode === 'pc'){
    listEl.classList.add('cols-4');
  }

  // 右下角「添加」入口：手机是主要用法（手机本来没法往电脑剪贴板里写），
  // PC 上同样保留——手动补一条文本 / 传一张图也用得上。
  syncAddBtn();
  applyFabPos();   // 恢复上次拖动到的位置

  // 切换卡（文本/图片 tabs）与访问地址卡片严格等宽，PC/H5 一致：测量 .share 的实际
  // 渲染宽度写入 --feed-w，CSS 用 width 消费。URL 长短因机器而异，动态测量才能永远对齐。
  var feedWidth = 0;
  function syncFeedWidth(){
    if (!shareEl) return;
    var w = Math.ceil(shareEl.getBoundingClientRect().width);
    if (w > 0 && Math.abs(w - feedWidth) > 0.5){
      feedWidth = w;
      document.body.style.setProperty('--feed-w', w + 'px');
    }
  }
  // URL 文本可能因窗口宽度换行/收缩，resize 时重新测量保持对齐
  window.addEventListener('resize', syncFeedWidth);

  function toast(msg){
    toastEl.textContent = msg;
    toastEl.classList.add('show');
    clearTimeout(toast._t);
    toast._t = setTimeout(function(){ toastEl.classList.remove('show'); }, 1600);
  }

  // 时间戳统一带上年月日
  function fmtTime(ms){
    var d = new Date(ms);
    var p = function(v){ return v<10 ? '0'+v : ''+v; };
    return d.getFullYear() + '-' + p(d.getMonth()+1) + '-' + p(d.getDate())
      + ' ' + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
  }

  // 告诉服务端「这条是从页面复制的，别再记一次」，避免产生重复记录。
  // 只有在服务本机（桌面浏览器）时才需要：手机复制走的不是这台电脑的剪贴板。
  function suppress(payload){
    if (!state.canClear) return Promise.resolve();
    return fetch('/api/suppress', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    }).catch(function(){});
  }

  function copyText(text, okMsg){
    // 先上报再写剪贴板：保证 lancoclipWatcher 轮询到新内容时抑制已生效
    suppress({ kind: 'text', content: text }).then(function(){ writeLancoclipText(text, okMsg); });
  }

  function writeLancoclipText(text, okMsg){
    var done = function(){ toast(okMsg || '已复制'); };
    if (navigator.clipboard && window.isSecureContext){
      navigator.clipboard.writeText(text).then(done).catch(function(){ legacy(text, done); });
    } else { legacy(text, done); }
  }
  function legacy(text, done){
    var ta = document.createElement('textarea');
    ta.value = text; ta.style.position='fixed'; ta.style.opacity='0';
    document.body.appendChild(ta); ta.focus(); ta.select();
    try { document.execCommand('copy'); done(); } catch(e){ toast('复制失败'); }
    document.body.removeChild(ta);
  }

  // 兜底：手动选中文字后 ⌘C / 右键「复制图片」也来自本页面，同样不记录
  document.addEventListener('copy', function(e){
    if (!state.canClear) return;
    var t = e.target;
    if (t && t.tagName === 'IMG' && t.dataset && t.dataset.id){
      suppress({ kind: 'image', id: Number(t.dataset.id) });
      return;
    }
    var sel = window.getSelection ? String(window.getSelection()) : '';
    if (sel) suppress({ kind: 'text', content: sel });
  });

  function fetchPage(page, size){
    return fetch('/api/records?type=' + state.type + '&page=' + page + '&size=' + size, { cache:'no-store' })
      .then(function(r){
        return r.json().then(function(j){
          // 403（Host 白名单）/ 502（反代）也可能返回 JSON。若不校验就
          // 把 undefined 写进 state.total，轮询会每 2s 重拉一次并刷屏提示。
          if (!r.ok || !j || !Array.isArray(j.records) || typeof j.total !== 'number'){
            var e = new Error('bad response');
            e.status = r.status;   // offline 据此区分 403 与网络故障
            throw e;
          }
          return j;
        });
      });
  }

  function sigOf(records){
    return JSON.stringify(records.map(function(r){ return [r.id, r.kind, r.content, r.imageSum]; }));
  }

  function render(records, append){
    if (!append) listEl.innerHTML = '';
    records.forEach(function(r){ appendItem(r); });
    emptyEl.hidden = state.total > 0;
    emptyEl.textContent = EMPTY_TEXT[state.type] || EMPTY_TEXT.text;
    countEl.textContent = countLabel();
  }

  function appendItem(r){
    var isImage = r.kind === 'image';
    var li = document.createElement('li');
    li.className = 'card item';
    li.dataset.id = r.id;   // 轮询去重：判断这条记录是否已在列表中
    // 内容指纹：清空后 ID 会重新编号，光比 ID 认不出「同号不同内容」的陈旧条目
    li.dataset.sum = r.imageSum || '';
    if (!isImage) li.dataset.text = r.content || '';

    // 头部一行：时间 + 展开（固定最左） + 弹性空隙 + 收藏 + 复制/保存
    var head = document.createElement('div');
    head.className = 'item-head';

    var t = document.createElement('span');
    t.className = 'time'; t.textContent = fmtTime(r.createdAt);
    head.appendChild(t);

    // 「展开/收起」永远固定在最左边（紧跟时间、spacer 之前），
    // 不夹在「收藏 / 取消收藏」和「复制 / 保存」中间。放在 spacer 左侧
    // 还能保证它显隐时右侧按钮不被推走。是否显示由末尾 applyClamp() 决定。
    var toggle = document.createElement('button');
    toggle.className = 'toggle';
    toggle.hidden = true;
    toggle.textContent = state.openIds[r.id] ? '收起' : '展开';
    head.appendChild(toggle);

    var spacer = document.createElement('span');
    spacer.className = 'spacer';
    head.appendChild(spacer);

    // 收藏只作用于文本：文本 tab 与收藏 tab 都有这颗按钮，
    // 收藏显示「取消收藏」，未收藏显示「收藏」。
    if (!isImage){
      var favBtn = document.createElement('button');
      favBtn.type = 'button';
      favBtn.className = 'btn sm fav' + (r.favorite ? ' on' : '');
      favBtn.textContent = r.favorite ? '取消收藏' : '收藏';
      favBtn.title = r.favorite ? '从收藏中移除' : '收藏这条文本';
      favBtn.addEventListener('click', function(){ toggleFavorite(r, favBtn, li); });
      head.appendChild(favBtn);
    }

    if (isImage){
      // 写图片到剪贴板依赖 ClipboardItem，手机浏览器基本不支持，故只在桌面端给出「复制」
      if (!isMobile){
        var copyImg = document.createElement('button');
        copyImg.className = 'btn sm';
        copyImg.textContent = '复制';
        copyImg.addEventListener('click', function(){ copyImageToLancoclip(r.id); });
        head.appendChild(copyImg);
      }
      var save = document.createElement('a');
      save.className = 'btn sm';
      save.textContent = '保存';
      save.href = '/api/image?id=' + r.id;
      save.setAttribute('download', 'lanoclip-' + r.id + '.png');
      head.appendChild(save);
    } else {
      var copyBtn = document.createElement('button');
      copyBtn.className = 'btn sm';
      copyBtn.textContent = '复制';
      copyBtn.addEventListener('click', function(){ copyText(r.content, '内容已复制'); });
      head.appendChild(copyBtn);
    }

    li.appendChild(head);

    // 内容体：文本用 <pre>，图片用 <img>
    var body;
    if (isImage){
      body = document.createElement('img');
      body.className = 'content shot';
      body.src = '/api/image?id=' + r.id;
      body.dataset.id = r.id;   // 右键「复制图片」时据此抑制重复记录
      body.alt = '剪贴板图片';
      body.loading = 'lazy';
    } else {
      body = document.createElement('pre');
      body.className = 'content';
      body.textContent = r.content;
    }
    var open = !!state.openIds[r.id];
    body.classList.add(open ? 'open' : 'clamp');
    li.appendChild(body);

    toggle.addEventListener('click', function(){
      var nowOpen = body.classList.toggle('open');
      body.classList.toggle('clamp', !nowOpen);
      if (nowOpen) { state.openIds[r.id] = true; toggle.textContent = '收起'; }
      else { delete state.openIds[r.id]; toggle.textContent = '展开'; }
    });

    listEl.appendChild(li);

    // 只有确实被截断时才显示「展开」。
    // 图片：固定高度 + contain，按当前渲染宽度换算完整高度，
    // 超出固定高度才需要展开（判据不依赖具体视口，PC 与手机结果一致）；
    // 文本：内容超过收起的一行才需要展开。
    var applyClamp = function(){
      if (open) return;
      var truncated = isImage
        ? (body.naturalWidth ? body.clientWidth * body.naturalHeight / body.naturalWidth : 0) > SHOT_H + 1
        : body.scrollHeight > body.clientHeight + 2;
      toggle.hidden = !truncated;
    };
    if (isImage){
      if (body.complete) applyClamp();
      else body.addEventListener('load', applyClamp, { once: true });
    } else {
      applyClamp();
    }
    return li;
  }

  // 收藏 / 取消收藏：先落服务端，再按结果改按钮。收藏 tab 上取消收藏后
  // 这条记录不再属于当前视图，直接从列表里摘掉并更新计数（不重拉整页，
  // 避免翻页位置跳动）；下一轮 poll 会按新集合重算签名、补齐分页。
  function toggleFavorite(r, btn, li){
    var want = !r.favorite;
    btn.disabled = true;
    fetch('/api/favorite', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: r.id, favorite: want })
    }).then(readJSON).then(function(res){
      btn.disabled = false;
      if (!res.ok){ toast((res.j && res.j.error) || '操作失败'); return; }
      r.favorite = want;
      btn.textContent = want ? '取消收藏' : '收藏';
      btn.title = want ? '从收藏中移除' : '收藏这条文本';
      btn.classList.toggle('on', want);
      if (typeof res.j.favorites === 'number'){
        state.favorites = res.j.favorites;
        badgeFavorite.textContent = state.favorites;
      }
      if (!want && state.type === 'favorite'){
        if (li.parentNode) li.parentNode.removeChild(li);
        state.total = Math.max(0, state.total - 1);
        countEl.textContent = countLabel();
        emptyEl.hidden = state.total > 0;
        state.sig = '';
        state.firstSig = '';   // 让下一轮 poll 重新取第 1 页，分页跟着收敛
      }
      toast(want ? '已收藏' : '已取消收藏');
    }).catch(function(){
      btn.disabled = false;
      toast('操作失败');
    });
  }

  // ── PC 分页器 ──────────────────────────────────────────────
  // 页码序列：总页数少时全列，多时用省略号收拢中间
  function pageButtons(cur, total){
    var out = [];
    if (total <= 7){
      for (var i = 1; i <= total; i++) out.push(i);
      return out;
    }
    out.push(1);
    var start = Math.max(2, cur - 1), end = Math.min(total - 1, cur + 1);
    if (start > 2) out.push('...');
    for (var j = start; j <= end; j++) out.push(j);
    if (end < total - 1) out.push('...');
    out.push(total);
    return out;
  }

  function renderPager(){
    if (mode !== 'pc' || state.totalPages <= 1){ pagerEl.hidden = true; return; }
    pagerEl.hidden = false;
    pagesEl.innerHTML = '';
    pageButtons(state.page, state.totalPages).forEach(function(p){
      if (p === '...'){
        var e = document.createElement('span');
        e.className = 'ellipsis'; e.textContent = '…';
        pagesEl.appendChild(e);
        return;
      }
      var b = document.createElement('button');
      b.className = 'btn sm page' + (p === state.page ? ' active' : '');
      b.textContent = p;
      b.addEventListener('click', function(){ gotoPage(p); });
      pagesEl.appendChild(b);
    });
    prevBtn.disabled = state.page <= 1;
    nextBtn.disabled = state.page >= state.totalPages;
  }

  function gotoPage(p){
    if (p < 1 || p > state.totalPages || p === state.page) return;
    state.page = p;
    loadView(true);
    window.scrollTo({ top: 0, behavior: 'smooth' });
  }

  // 切换 tab：重置分页状态后重新拉取（两个类型互不影响）
  function switchTab(type){
    if (type === state.type) return;
    state.type = type;
    state.page = 1;
    state.pages = 1;
    state.sig = '';
    state.firstSig = '';
    state.openIds = {};
    syncAddBtn();   // 悬浮按钮跟着 tab 换图标（+ → 图片）
    listEl.innerHTML = '';
    loadmoreEl.hidden = true;
    pagerEl.hidden = true;
    window.scrollTo({ top: 0, behavior: 'smooth' });
    loadView(true);
  }

  // ── H5 上滑加载更多 ────────────────────────────────────────
  function setLoadMore(txt){ loadmoreEl.textContent = txt; }

  function updateLoadMore(hasMore){
    if (state.total === 0){ loadmoreEl.hidden = true; return; }
    loadmoreEl.hidden = false;
    if (hasMore){
      setLoadMore('上滑加载更多');
      // 内容不足一屏时自动补一页
      setTimeout(function(){
        if (!state.hasMore || state.loading) return;
        if (window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 240) loadMoreH5();
      }, 50);
    } else {
      setLoadMore('没有更多数据了');
    }
  }

  function loadMoreH5(){
    if (mode !== 'h5' || state.loading || !state.hasMore) return;
    state.loading = true;
    setLoadMore('加载中…');
    fetchPage(state.pages + 1, pageSize()).then(function(data){
      state.loading = false;
      state.pages++;
      state.total = data.total;
      state.totalPages = data.totalPages;
      state.hasMore = data.hasMore;
      state.sig += '||' + sigOf(data.records);
      render(data.records, true);
      updateLoadMore(state.hasMore);
    }).catch(function(){
      state.loading = false;
      setLoadMore('加载失败，点上滑重试');
    });
  }

  // ── 加载 / 轮询 ────────────────────────────────────────────
  function onMeta(data){
    // 设置弹窗标题旁的版本徽标：v版本 · 构建时间（本地开发构建 buildTime=dev 时只显示版本）
    if (data.version){
      var label = 'v' + data.version;
      if (data.buildTime && data.buildTime !== 'dev') label += ' · ' + data.buildTime;
      if (settingsVerEl.textContent !== label){
        settingsVerEl.textContent = label;
        settingsVerEl.hidden = false;
      }
    }

    // tab 上的数量
    if (data.counts){
      badgeText.textContent = data.counts.text || 0;
      badgeImage.textContent = data.counts.image || 0;
      state.favorites = data.counts.favorite || 0;
      badgeFavorite.textContent = state.favorites;
    }
    // 保留条数配置（设置弹窗回填用）
    if (data.settings){
      state.settings.maxText = data.settings.maxText || state.settings.maxText;
      state.settings.maxImage = data.settings.maxImage || state.settings.maxImage;
    }
    // 暂停状态 / 写盘异常随轮询同步；状态变化时只弹一次 toast，横幅常驻。
    var paused = !!data.paused;
    // 用户正在弹窗里拨开关时，轮询 / 托盘状态更新不能把他的选择打回旧值。
    var userEdited = pausedInput.checked !== state.settings.paused;
    if (paused !== state.settings.paused){
      state.settings.paused = paused;
      if (paused) toast('已暂停记录');
    }
    if (!userEdited) pausedInput.checked = paused;
    var storageErr = data.storageError || '';
    if (storageErr !== state.storageError){
      state.storageError = storageErr;
      if (storageErr) toast('存储异常：' + storageErr);
    }
    var notice = paused ? '已暂停记录：不再保存新的剪贴板内容'
               : (storageErr ? '存储异常：' + storageErr : '');
    if (notice){
      if (statusNotice.textContent !== notice) statusNotice.textContent = notice;
      statusNotice.hidden = false;
    } else {
      statusNotice.hidden = true;
    }
    var isText = state.type === 'text';
    var isImage = state.type === 'image';
    var isFav = state.type === 'favorite';
    tabText.classList.toggle('active', isText);
    tabImage.classList.toggle('active', isImage);
    tabFavorite.classList.toggle('active', isFav);
    tabText.setAttribute('aria-selected', isText ? 'true' : 'false');
    tabImage.setAttribute('aria-selected', isImage ? 'true' : 'false');
    tabFavorite.setAttribute('aria-selected', isFav ? 'true' : 'false');

    // clearBtn 的文案跟着 tab 走：清空全部文本 / 清空全部图片；收藏页隐藏它，
    // 换成同位置的「清空全部收藏」按钮（三个按钮命名统一、位置一致）。
    // 注意「清空全部收藏」只移除收藏标记、不删除文本记录，语义与另两者不同。
    var clearLabel = isImage ? '清空全部图片' : '清空全部文本';
    if (clearBtn.textContent !== clearLabel) clearBtn.textContent = clearLabel;

    state.canClear = !!data.canClear && !isMobile;

    // 托盘「设置」以 ?settings=1 打开本页：权限就绪后自动弹出设置弹窗（仅一次）
    if (autoOpenSettings && state.canClear){
      autoOpenSettings = false;
      openSettings();
    }
    // 快捷键触发：?qr=1 直接弹二维码；?history=1 打开历史记录浮层（均只弹一次）
    if (autoOpenQR && data.shareUrl){
      autoOpenQR = false;
      openModal();
    }
    if (autoOpenHistory && data.shareUrl){
      autoOpenHistory = false;
      openHistory();
    }

    if (data.shareUrl && data.shareUrl !== state.shareUrl){
      state.shareUrl = data.shareUrl;
      shareUrlEl.textContent = data.shareUrl;
      qrUrl.textContent = data.shareUrl;
      qrImg.src = '/api/qr?text=' + encodeURIComponent(data.shareUrl);
      syncFeedWidth(); // URL 文本落地后 .share 才有真实宽度，此时对齐列表列宽
    }

    // 手机端把按钮从 DOM 里彻底移除，不给任何重新显示的机会。
    // 设置入口 / 清空全部文本·图片 / 清空全部收藏同一套权限：仅 PC + 请求来自服务本机时显示。
    // 两个清空按钮互斥：收藏页只显示「清空全部收藏」，文本/图片页只显示 clearBtn。
    [settingsBtn, clearBtn, unfavAllBtn].forEach(function(b){
      if (!b) return;
      if (isMobile && b.parentNode){
        b.parentNode.removeChild(b);
      } else {
        var visible = state.canClear && (b !== clearBtn || !isFav)
                                     && (b !== unfavAllBtn || isFav);
        b.hidden = !visible;
      }
    });
    // 没有收藏时按钮置灰，不发无意义的请求（收藏页的 total 就是收藏数）
    if (unfavAllBtn && unfavAllBtn.parentNode){
      unfavAllBtn.disabled = isFav && !data.total;
    }
  }

  // 断线反馈：状态行已移除，改用页面已有的 toast 提示，不断线时静默。
  // 带 err.status 时区分提示：403（Host/跨站拦截）不是网络问题。
  function offline(err){
    if (err && err.status === 403){
      toast('没有权限访问本页');
      return;
    }
    toast('连接断开，重试中…');
  }

  // 拉取并渲染「当前视图」：PC = 当前页；H5 = 已加载的全部页。
  // 返回 Promise<boolean>：调用方（poll）据此决定是否回滚 firstSig。
  function loadView(force){
    // 被并发加载挡住时返回 false：让调用方（poll）回滚签名，下一轮重试。
    // 返回 true 会让 poll 误以为本次渲染已成功，视图可能停滞到手动刷新。
    if (state.loading) return Promise.resolve(false);
    state.loading = true;
    var req = (mode === 'pc')
      ? fetchPage(state.page, pageSize())
      : fetchPage(1, pageSize() * state.pages);

    return req.then(function(data){
      state.loading = false;
      state.total = data.total;
      state.totalPages = data.totalPages;
      state.hasMore = data.hasMore;
      // 记录被清空 / 删除导致当前页越界时回到最后一页
      if (mode === 'pc' && state.page > data.totalPages){
        state.page = data.totalPages;
        state.loading = false;
        return loadView(true);
      }
      onMeta(data);

      var sig = sigOf(data.records);
      // 同步轮询用的「第 1 页签名」。切 tab 时 firstSig 被清空，如果这里不补上，
      // 紧跟着的那次 poll 会以为数据变了、再拉一遍（切一次 tab 请求两次）。
      // PC 只有在停在第 1 页时才有第 1 页的数据可比；H5 一次取的是多页，
      // 取前 pageSize() 条才是第 1 页。
      if (mode === 'pc'){
        if (state.page === 1) state.firstSig = sig;
      } else {
        state.firstSig = sigOf((data.records || []).slice(0, pageSize()));
      }
      if (force || sig !== state.sig){
        state.sig = sig;
        render(data.records, false);
      }
      if (mode === 'pc') renderPager();
      else { state.pages = Math.max(1, Math.ceil(data.records.length / pageSize())); updateLoadMore(state.hasMore); }
      return true;
    }).catch(function(){
      // 关键：失败时必须解锁，否则 state.loading 永远为 true，之后所有
      // loadView 被开头的 return 挡死，页面再也不会刷新（必须手动刷新）。
      state.loading = false;
      offline();
      return false;
    });
  }

  // 轮询只看第 1 页：有变化再更新当前视图，既省流量又不打断阅读
  function poll(){
    fetchPage(1, pageSize()).then(function(data){
      var sig = sigOf(data.records);
      // 记录变少 = 电脑上清空了 / 上限淘汰了旧条目。此时不能只做增量插入：
      // 消失的条目在 H5 上会一直挂在列表里（prependNewRecords 只增不删），
      // 表现就是「电脑上清空了，手机上还显示着」——必须整屏重画。
      var shrunk = data.total < state.total;
      if (sig !== state.firstSig || data.total !== state.total){
        if (mode === 'h5' && !shrunk){
          prependNewRecords(data);  // H5 增量插入，不清空、不动滚动位置（内部同步 firstSig）
        } else if (mode === 'pc' && state.page > 1 && !shrunk){
          state.firstSig = sig;     // 在别的页：只记下签名，不打断阅读
          // total / totalPages 也必须同步，否则 state.total 永远落后：
          // 之后每次 poll 都因 total 不等被当成「有变化」，反复空转。
          state.total = data.total;
          state.totalPages = data.totalPages;
          onMeta(data);
        } else {
          // 先记签名避免反复重画，但渲染失败要回滚，让下一次 poll 重试。
          var prevSig = state.firstSig;
          state.firstSig = sig;
          loadView(true).then(function(ok){ if (!ok) state.firstSig = prevSig; });
        }
      } else {
        onMeta(data);
      }
    }).catch(offline);
  }

  // H5 轮询到新记录：只把新记录插到列表顶部，不清空列表、不重置滚动位置。
  // 旧实现走 loadView(false) 全量重画（innerHTML=''），会把用户瞬间甩回顶部，
  // 还会把「上滑加载更多」已加载出来的多页数据一起抹掉。
  function prependNewRecords(data){
    onMeta(data);
    state.total = data.total;
    state.firstSig = sigOf(data.records || []);  // 已渲染的就是第 1 页，别让下次 poll 空转
    var known = {}, byId = {};
    var nodes = listEl.children;
    for (var i = 0; i < nodes.length; i++){
      var id = nodes[i].dataset && nodes[i].dataset.id;
      if (id){ known[id] = true; byId[id] = nodes[i]; }
    }
    var fresh = [], stale = false;
    (data.records || []).forEach(function(r){
      var node = byId[r.id];
      if (!node){ fresh.push(r); return; }
      // 同 ID 但内容已经不是同一条：清空后 ID 会重新编号，只按 ID 去重会
      // 把已被清掉的旧条目当成「已渲染」而漏掉新的——此时必须整屏重画。
      var same = (r.kind === 'image')
        ? (node.dataset.sum || '') === (r.imageSum || '')
        : (node.dataset.text || '') === (r.content || '');
      if (!same) stale = true;
    });
    if (stale){
      // 与 poll 的 else 分支同样处理：渲染失败要回滚 firstSig，
      // 否则视图会停留在「同 ID 不同内容」的陈旧状态直到手动操作。
      var prevSig = state.firstSig;
      loadView(true).then(function(ok){ if (!ok) state.firstSig = prevSig; });
      return;
    }
    if (fresh.length){
      // 服务端返回「最新在前」：倒序插入，最终 DOM 顺序才是「最新的在最上面」
      for (var i = fresh.length - 1; i >= 0; i--){
        listEl.insertBefore(appendItem(fresh[i]), listEl.firstChild);
      }
    }
    emptyEl.hidden = state.total > 0;
    countEl.textContent = countLabel();
  }

  // 托盘「设置」/ 快捷键是以 ?settings=1 / ?qr=1 / ?history=1 打开本页的。
  // 弹窗关掉后要把这个参数从地址栏抹掉：否则浏览器里一直留着一个同 URL 的
  // 标签页，下次再点托盘「设置」只会切换到它、不会重新加载页面，弹窗也就不
  // 会再出现（看起来像「点了设置没反应」）。
  function clearAutoParam(){
    if (!/[?&](settings|qr|history)=1/.test(location.search)) return;
    var kept = location.search.replace(/^\?/, '').split('&').filter(function(kv){
      return kv && kv !== 'settings=1' && kv !== 'qr=1' && kv !== 'history=1';
    });
    try {
      history.replaceState(null, '', location.pathname + (kept.length ? '?' + kept.join('&') : ''));
    } catch(e) {}
  }

  function openModal(){ modal.hidden = false; }
  function closeModal(){ modal.hidden = true; clearAutoParam(); }

  // ── 统一确认弹窗（页面居中）────────────────────────────────
  // 代替 window.confirm：内置弹窗样式不可控、出现在窗口顶部而不是页面中间，
  // 也没法统一危险按钮的样式。清空全部文本 / 图片 / 收藏共用这一套。
  // 返回 Promise<boolean>：确认 = true，取消 / ESC / 点遮罩 = false。
  var confirmResolve = null;
  function closeConfirm(ok){
    if (!confirmResolve) return;
    var resolve = confirmResolve;
    confirmResolve = null;
    confirmModal.hidden = true;
    resolve(!!ok);
  }
  function askConfirm(opts){
    // 上一个还没关就先当作「取消」，避免旧 Promise 永远悬着
    if (confirmResolve) closeConfirm(false);
    confirmTitleEl.textContent = opts.title || '确认操作';
    confirmTextEl.textContent = opts.message || '';
    // 确认按钮统一写「确认」，不再按操作定制按钮文案。
    // 具体操作由标题与说明交代，避免三个弹窗出现三套按钮叫法。
    confirmOkBtn.textContent = '确认';
    confirmModal.hidden = false;
    // 破坏性操作默认焦点放「取消」：连按 Enter 不会误触发
    try { confirmCancelBtn.focus(); } catch(e){}
    return new Promise(function(resolve){ confirmResolve = resolve; });
  }
  confirmOkBtn.addEventListener('click', function(){ closeConfirm(true); });
  confirmCancelBtn.addEventListener('click', function(){ closeConfirm(false); });
  confirmModal.addEventListener('click', function(e){ if (e.target === confirmModal) closeConfirm(false); });

  // ── 设置弹窗：通用 / 快捷键 双 tab ────────────────────────
  // 快捷键可自定义：点击组合键框 → 按下新组合 → 松开后回显；保存后服务端
  // 重注册（canonical 形如 "meta+shift+q"，meta 在 Mac 上是 ⌘、Windows 上是 Win 键）
  var isMac = /Mac|iPhone|iPad|iPod/i.test(UA);
  var hkModNames = isMac ? { meta:'⌘', ctrl:'⌃', alt:'⌥', shift:'⇧' }
                         : { meta:'Win', ctrl:'Ctrl', alt:'Alt', shift:'Shift' };
  // 未拿到服务端值前先按平台默认显示
  state.settings.hotkeyQR = isMac ? 'meta+shift+q' : 'ctrl+shift+q';
  state.settings.hotkeyHistory = isMac ? 'meta+shift+v' : 'ctrl+shift+v';
  var pendingHK = { qr: null, hist: null }; // 捕获到、尚未保存的组合键
  var hkCapture = null;                     // { which:'qr'|'hist', mods:{}, key:null }

  function fmtCombo(str){
    if (!str) return '—';
    return str.split('+').map(function(t){
      if (hkModNames[t]) return hkModNames[t];
      if (t === 'space') return 'Space';
      return t.toUpperCase();
    }).join(' + ');
  }
  function setHKLabel(which, text){
    (which === 'qr' ? hkQR : hkHistory).textContent = text;
  }
  function refreshHK(){
    setHKLabel('qr', fmtCombo(pendingHK.qr !== null ? pendingHK.qr : state.settings.hotkeyQR));
    setHKLabel('hist', fmtCombo(pendingHK.hist !== null ? pendingHK.hist : state.settings.hotkeyHistory));
  }
  function keyToken(e){
    var k = e.key;
    if (/^[a-z]$/i.test(k)) return k.toLowerCase();
    if (/^[0-9]$/.test(k)) return k;
    if (/^f([1-9]|1[0-9]|2[0-4])$/i.test(k)) return k.toLowerCase();
    if (k === ' ') return 'space';
    if (['-','=','[',']',';',"'",',','.','/','\\','`'].indexOf(k) >= 0) return k;
    return null;
  }
  function isModKey(k){ return k === 'Meta' || k === 'Control' || k === 'Alt' || k === 'Shift'; }
  function canonCombo(mods, key){
    var parts = [];
    if (mods.meta) parts.push('meta');
    if (mods.ctrl) parts.push('ctrl');
    if (mods.alt) parts.push('alt');
    if (mods.shift) parts.push('shift');
    if (!parts.length) return '';
    parts.push(key);
    return parts.join('+');
  }

  function startHKCapture(which){
    if (!state.canClear || isMobile){
      toast('请在运行服务的电脑上修改快捷键');
      return;
    }
    endHKCapture();
    hkCapture = { which: which, mods: null, key: null };
    (which === 'qr' ? hkQRBtn : hkHistBtn).classList.add('listening');
    setHKLabel(which, '请按下组合键…');
  }
  function endHKCapture(){
    if (!hkCapture) return;
    hkCapture = null;
    hkQRBtn.classList.remove('listening');
    hkHistBtn.classList.remove('listening');
    refreshHK();
  }
  function hkKeydown(e){
    if (!hkCapture) return;
    e.preventDefault(); e.stopImmediatePropagation();
    if (e.key === 'Escape'){ endHKCapture(); toast('已取消修改'); return; }
    var mods = { meta: e.metaKey, ctrl: e.ctrlKey, alt: e.altKey, shift: e.shiftKey };
    var tok = keyToken(e);
    if (tok){
      hkCapture.mods = mods; hkCapture.key = tok;
      // 实时回显（即使还缺修饰键，松手后统一校验提示）
      setHKLabel(hkCapture.which, fmtCombo(canonCombo(mods, tok) || tok));
      return;
    }
    if (isModKey(e.key)){
      hkCapture.mods = mods;
      var parts = [];
      if (mods.meta) parts.push(hkModNames.meta);
      if (mods.ctrl) parts.push(hkModNames.ctrl);
      if (mods.alt) parts.push(hkModNames.alt);
      if (mods.shift) parts.push(hkModNames.shift);
      setHKLabel(hkCapture.which, (parts.length ? parts.join(' + ') + ' + ' : '') + '…');
    }
  }
  function hkKeyup(e){
    if (!hkCapture) return;
    e.preventDefault(); e.stopImmediatePropagation();
    var tok = keyToken(e);
    // 修饰键先松开、或主键未按下：继续等待
    if (!tok || tok !== hkCapture.key) return;
    var combo = canonCombo(hkCapture.mods, tok);
    var which = hkCapture.which;
    hkCapture = null;
    hkQRBtn.classList.remove('listening');
    hkHistBtn.classList.remove('listening');
    if (!combo){
      toast('需要包含 Cmd/Ctrl/Alt 修饰键，已取消');
      refreshHK();
      return;
    }
    pendingHK[which] = combo;
    setHKLabel(which, fmtCombo(combo));
  }
  // 捕获阶段注册：按下组合键期间拦截页面所有其他键盘行为
  document.addEventListener('keydown', hkKeydown, true);
  document.addEventListener('keyup', hkKeyup, true);
  window.addEventListener('blur', endHKCapture);
  hkQRBtn.addEventListener('click', function(){ startHKCapture('qr'); });
  hkHistBtn.addEventListener('click', function(){ startHKCapture('hist'); });

  function showSettingsTab(which){
    var general = which === 'general';
    setTabGeneral.classList.toggle('active', general);
    setTabHotkey.classList.toggle('active', !general);
    setTabGeneral.setAttribute('aria-selected', general ? 'true' : 'false');
    setTabHotkey.setAttribute('aria-selected', general ? 'false' : 'true');
    setPaneGeneral.hidden = !general;
    setPaneHotkey.hidden = general;
  }
  setTabGeneral.addEventListener('click', function(){ showSettingsTab('general'); });
  setTabHotkey.addEventListener('click', function(){ showSettingsTab('hotkey'); });

  // 保存快捷键：只提交 hotkeyQR / hotkeyHistory，服务端校验并重注册
  hotkeySave.addEventListener('click', function(){
    if (hkCapture) endHKCapture();
    var changed = pendingHK.qr !== null || pendingHK.hist !== null;
    if (!changed){ closeSettings(); return; }
    var payload = {
      hotkeyQR: pendingHK.qr !== null ? pendingHK.qr : state.settings.hotkeyQR,
      hotkeyHistory: pendingHK.hist !== null ? pendingHK.hist : state.settings.hotkeyHistory,
    };
    fetch('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    }).then(function(r){
      return r.json().then(function(j){ return { ok: r.ok, j: j }; });
    }).then(function(res){
      if (!res.ok){ toast(res.j.error || '保存失败'); return; }
      state.settings.hotkeyQR = res.j.hotkeyQR;
      state.settings.hotkeyHistory = res.j.hotkeyHistory;
      pendingHK = { qr: null, hist: null };
      refreshHK();
      toast('快捷键已保存，立即生效');
    }).catch(function(){ toast('操作失败'); });
  });
  // 还原：丢弃未保存的修改（endHKCapture 在未捕获时是 no-op，需主动刷新显示）
  hotkeyCancel.addEventListener('click', function(){
    pendingHK = { qr: null, hist: null };
    endHKCapture();
    refreshHK();
  });

  function openSettings(){
    showSettingsTab('general');
    maxTextInput.value = state.settings.maxText;
    maxImageInput.value = state.settings.maxImage;
    // 自启状态实时拉取（可能在程序外被改动）；无权限/手机端直接隐藏该行
    autostartField.hidden = !state.canClear;
    autostartInput.checked = !!state.settings.autostart;
    // 暂停状态可能被托盘菜单改过，打开弹窗时先按已知状态回填，再实时拉取
    pausedField.hidden = !state.canClear;
    pausedInput.checked = state.settings.paused;
    // 快捷键：还原未保存修改、按权限显隐捕获框，并实时拉取当前组合
    endHKCapture();
    pendingHK = { qr: null, hist: null };
    refreshHK();
    hkQRBtn.hidden = !state.canClear || isMobile;
    hkHistBtn.hidden = hkQRBtn.hidden;
    if (state.canClear){
      fetch('/api/settings', { cache: 'no-store' }).then(function(r){ return r.json(); }).then(function(j){
        // 异步回填可能晚于用户点击：已经动过开关就别覆盖（与 paused 同理）。
        var autostartEdited = autostartInput.checked !== state.settings.autostart;
        state.settings.autostartSupported = !!j.autostartSupported;
        state.settings.autostart = !!j.autostart;
        autostartField.hidden = !state.canClear || !state.settings.autostartSupported;
        if (!autostartEdited) autostartInput.checked = state.settings.autostart;
        if (j.paused !== undefined){
          // 异步回填可能晚于用户点击：已经动过开关就别覆盖。
          var edited = pausedInput.checked !== state.settings.paused;
          state.settings.paused = !!j.paused;
          if (!edited) pausedInput.checked = state.settings.paused;
        }
        if (j.hotkeyQR) state.settings.hotkeyQR = j.hotkeyQR;
        if (j.hotkeyHistory) state.settings.hotkeyHistory = j.hotkeyHistory;
        refreshHK();
      }).catch(function(){});
    }
    settingsModal.hidden = false;
  }
  function closeSettings(){ settingsModal.hidden = true; clearAutoParam(); }

  document.getElementById('qrBtn').addEventListener('click', openModal);
  document.getElementById('qrClose').addEventListener('click', closeModal);
  modal.addEventListener('click', function(e){ if (e.target === modal) closeModal(); });
  settingsClose.addEventListener('click', closeSettings);
  settingsCancel.addEventListener('click', closeSettings);
  settingsModal.addEventListener('click', function(e){ if (e.target === settingsModal) closeSettings(); });

  // ── 历史记录浮层（?history=1 / 快捷键触发）────────────────
  // 最近 30 条（与托盘「历史」一致）：文本预览或图片缩略图，点击复制回剪贴板
  function fmtHistTime(ms){
    var d = new Date(ms), now = new Date();
    var pad = function(n){ return n < 10 ? '0' + n : '' + n; };
    var hm = pad(d.getHours()) + ':' + pad(d.getMinutes());
    return (d.toDateString() === now.toDateString()) ? hm
      : (pad(d.getMonth() + 1) + '-' + pad(d.getDate()) + ' ' + hm);
  }

  function copyImageToLancoclip(id){
    // 与列表页一致：先上报抑制（本机复制不再入库），再写剪贴板
    suppress({ kind: 'image', id: id }).then(function(){
      fetch('/api/image?id=' + id).then(function(r){ return r.blob(); }).then(function(blob){
        // navigator.clipboard 只在安全上下文存在：用 http://192.168.x.x 这类
        // 局域网地址打开本页时它是 undefined（文本有 execCommand 兜底，图片没有）。
        if (navigator.clipboard && window.isSecureContext && typeof ClipboardItem !== 'undefined'){
          navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })])
            .then(function(){ toast('图片已复制'); })
            .catch(function(){ fallbackCopyImage(id); });   // 写入被拒也走兜底
          return;
        }
        fallbackCopyImage(id);
      }).catch(function(){ toast('复制图片失败'); });
    });
  }

  // 非安全上下文下的图片复制兜底：选中一张临时 <img> 再触发原生复制。
  // Chromium 内核（Edge / Chrome）可用，Safari 不支持；都不行时给出可操作提示。
  function fallbackCopyImage(id){
    if (copyImageViaExecCommand(id)){ toast('图片已复制'); return; }
    var local = location.protocol + '//127.0.0.1' + (location.port ? ':' + location.port : '');
    toast('复制失败：改用 ' + local + ' 打开本页，或点「保存」下载原图');
  }

  function copyImageViaExecCommand(id){
    try{
      var box = document.createElement('div');
      box.contentEditable = 'true';
      box.style.cssText = 'position:fixed;left:-9999px;top:0;width:1px;height:1px;overflow:hidden;';
      var img = document.createElement('img');
      img.src = '/api/image?id=' + id;
      box.appendChild(img);
      document.body.appendChild(box);
      var range = document.createRange();
      range.selectNodeContents(box);
      var sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
      var ok = document.execCommand('copy');
      sel.removeAllRanges();
      document.body.removeChild(box);
      return ok;
    }catch(e){ return false; }
  }

  function openHistory(){
    histList.innerHTML = '';
    histEmpty.hidden = true;
    historyModal.hidden = false;
    fetch('/api/recent?limit=30', { cache: 'no-store' })
      .then(function(r){ return r.json(); })
      .then(function(data){
        var recs = data.records || [];
        if (!recs.length){ histEmpty.hidden = false; return; }
        recs.forEach(function(r){
          var li = document.createElement('li');
          li.className = 'hist-item';
          var t = document.createElement('span');
          t.className = 'hist-time';
          t.textContent = fmtHistTime(r.createdAt);
          li.appendChild(t);
          if (r.kind === 'image'){
            var img = document.createElement('img');
            img.className = 'hist-thumb';
            img.src = '/api/image?id=' + r.id;
            img.alt = '[图片]';
            img.loading = 'lazy';
            li.appendChild(img);
            var label = document.createElement('span');
            label.className = 'hist-text';
            label.textContent = '[图片]';
            li.appendChild(label);
            li.addEventListener('click', function(){ copyImageToLancoclip(r.id); });
          } else {
            var text = document.createElement('span');
            text.className = 'hist-text';
            var content = String(r.content || '');
            text.textContent = content.length > 80 ? content.slice(0, 80) + '…' : content;
            li.title = content;
            li.appendChild(text);
            li.addEventListener('click', function(){ copyText(content, '已复制'); });
          }
          histList.appendChild(li);
        });
      })
      .catch(function(){ toast('加载历史记录失败'); });
  }
  function closeHistory(){ historyModal.hidden = true; clearAutoParam(); }
  historyClose.addEventListener('click', closeHistory);
  historyModal.addEventListener('click', function(e){ if (e.target === historyModal) closeHistory(); });

  // ── 手机端「添加」：文本 tab 写一条，图片 tab 从相册/文件选一张 ──────
  // 两条路都直接写进服务本机的记录（/api/add-text、/api/add-image），
  // 不经过系统剪贴板，因此不需要向 /api/suppress 上报抑制。
  function syncAddBtn(){
    if (!addBtn) return;
    var isImage = state.type === 'image';
    addBtn.classList.toggle('pick-image', isImage);
    var label = isImage ? '上传图片' : '添加文本';
    addBtn.title = label;
    addBtn.setAttribute('aria-label', label);
    // 收藏是筛选视图，没有「往收藏里添加」这回事
    addBtn.hidden = state.type === 'favorite';
  }

  // ── 悬浮按钮拖动：挡住内容时挪开，位置存 localStorage ──────────
  // 存的是视口内的 px 坐标，恢复时按当前窗口夹取，避免换设备 / 转屏 /
  // 改窗口大小后按钮跑到屏幕外面。
  function clampFab(x, y){
    var w = addBtn.offsetWidth || 54, h = addBtn.offsetHeight || 54;
    var maxX = Math.max(8, window.innerWidth - w - 8);
    var maxY = Math.max(8, window.innerHeight - h - 8);
    return [Math.min(Math.max(8, x), maxX), Math.min(Math.max(8, y), maxY)];
  }

  function setFabPos(x, y){
    var p = clampFab(x, y);
    addBtn.style.left = p[0] + 'px';
    addBtn.style.top = p[1] + 'px';
    addBtn.style.right = 'auto';   // 一旦手动定位就脱离右下角默认布局
    addBtn.style.bottom = 'auto';
    return p;
  }

  function applyFabPos(){
    if (!addBtn) return;
    var raw = null;
    try { raw = localStorage.getItem(FAB_POS_KEY); } catch(e){ return; }
    if (!raw) return;
    var parts = raw.split(',');
    var x = parseFloat(parts[0]), y = parseFloat(parts[1]);
    if (!isFinite(x) || !isFinite(y)) return;
    setFabPos(x, y);
  }

  function openAddText(){
    addTextModal.hidden = false;
    addTextInput.value = '';
    // 弹窗刚显示时聚焦常被系统键盘顶掉，等一帧再聚焦更稳
    setTimeout(function(){ addTextInput.focus(); }, 60);
  }
  function closeAddText(){ addTextModal.hidden = true; }

  // 新记录一定排在最上面：H5 走增量插入后回到顶部，PC 直接重拉当前页
  function afterAdd(){
    if (mode === 'h5'){
      state.firstSig = '';   // 清空签名，下一轮 poll 才会判定「有新数据」
      window.scrollTo({ top: 0, behavior: 'smooth' });
      poll();
    } else {
      state.sig = '';
      loadView(true);
    }
  }

  function readJSON(r){
    // 统一收口：非 JSON 响应也不至于让 .then 链断掉
    return r.json().catch(function(){ return {}; }).then(function(j){ return { ok: r.ok, j: j }; });
  }

  function submitAddText(){
    var content = addTextInput.value;
    if (!content.trim()){ toast('请输入内容'); return; }
    addTextSubmit.disabled = true;
    fetch('/api/add-text', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: content })
    }).then(readJSON).then(function(res){
      addTextSubmit.disabled = false;
      if (!res.ok){ toast(res.j.error || '添加失败'); return; }
      closeAddText();
      // added=false：与最新一条重复，store 没变，不算失败
      toast(res.j.paused ? '已暂停记录' : (res.j.added === false ? '与最新一条重复' : '已添加到电脑'));
      afterAdd();
    }).catch(function(){
      addTextSubmit.disabled = false;
      toast('添加失败');
    });
  }

  function uploadImage(file){
    if (file.size > 32 * 1024 * 1024){ toast('图片超过 32MB'); return; }
    var fd = new FormData();
    fd.append('image', file, file.name || 'upload.png');
    toast('上传中…');
    fetch('/api/add-image', { method: 'POST', body: fd })
      .then(readJSON).then(function(res){
        if (!res.ok){ toast(res.j.error || '上传失败'); return; }
        toast(res.j.paused ? '已暂停记录' : (res.j.added === false ? '与最新一条重复' : '图片已上传'));
        afterAdd();
      }).catch(function(){ toast('上传失败'); });
  }

  if (addBtn){
    // 拖动状态：按下记起点，移动超过阈值才算拖动（否则当成点击）。
    // pointer 事件同时覆盖鼠标与触摸；setPointerCapture 保证手指/鼠标移出
    // 按钮后仍然收得到 move/up。
    var fabDrag = null;
    var fabMoved = false;

    addBtn.addEventListener('pointerdown', function(e){
      if (e.button != null && e.button !== 0) return;   // 只响应左键 / 触摸
      fabMoved = false;
      var r = addBtn.getBoundingClientRect();
      fabDrag = { id: e.pointerId, dx: e.clientX - r.left, dy: e.clientY - r.top,
                  sx: e.clientX, sy: e.clientY, x: r.left, y: r.top, moved: false };
      try { addBtn.setPointerCapture(e.pointerId); } catch(err){}
    });

    addBtn.addEventListener('pointermove', function(e){
      if (!fabDrag || e.pointerId !== fabDrag.id) return;
      if (!fabDrag.moved &&
          Math.abs(e.clientX - fabDrag.sx) + Math.abs(e.clientY - fabDrag.sy) < 6) return;
      fabDrag.moved = true;
      addBtn.classList.add('dragging');
      fabDrag.x = e.clientX - fabDrag.dx;
      fabDrag.y = e.clientY - fabDrag.dy;
      setFabPos(fabDrag.x, fabDrag.y);
      if (e.cancelable) e.preventDefault();   // 手机上别把拖动当成页面滚动
    });

    function endFabDrag(e){
      if (!fabDrag || (e && e.pointerId !== fabDrag.id)) return;
      var moved = fabDrag.moved, x = fabDrag.x, y = fabDrag.y;
      fabDrag = null;
      addBtn.classList.remove('dragging');
      if (!moved) return;
      fabMoved = true;              // 让随后的 click 失效（点击与拖动区分开）
      var p = setFabPos(x, y);      // 松手时再夹一次并落到最终位置
      try { localStorage.setItem(FAB_POS_KEY, p[0] + ',' + p[1]); } catch(err){}
    }
    addBtn.addEventListener('pointerup', endFabDrag);
    addBtn.addEventListener('pointercancel', endFabDrag);

    addBtn.addEventListener('click', function(){
      if (fabMoved){ fabMoved = false; return; }   // 刚刚是拖动，不是点击
      if (state.type === 'image'){
        imagePicker.value = '';  // 清掉旧值，否则连续选同一张不再触发 change
        imagePicker.click();
      } else {
        openAddText();
      }
    });

    // 窗口尺寸变化（转屏 / 改窗口）后把按钮拉回可视区域
    window.addEventListener('resize', applyFabPos);
  }
  imagePicker.addEventListener('change', function(){
    var f = imagePicker.files && imagePicker.files[0];
    if (f) uploadImage(f);
  });
  addTextSubmit.addEventListener('click', submitAddText);
  // 取消 / 右上角 × / 点遮罩：三个关闭入口，行为一致
  addTextCancel.addEventListener('click', closeAddText);
  addTextClose.addEventListener('click', closeAddText);
  addTextModal.addEventListener('click', function(e){ if (e.target === addTextModal) closeAddText(); });

  // ── 亮色 / 暗色切换 ────────────────────────────────────────
  // 默认跟随系统（首帧由 head 内联脚本决定）；用户手动点过按钮后，
  // 选择存入 localStorage，此后按用户设置展示、不再跟随系统变化。
  // 手机端同样可用（纯展示偏好，不涉及管理权限）。
  var THEME_KEY = 'lanoclip-theme';
  var darkMedia = matchMedia('(prefers-color-scheme: dark)');
  function isDark(){ return document.documentElement.getAttribute('data-theme') === 'dark'; }
  function applyTheme(mode){ // 'system' | 'light' | 'dark'
    var dark = mode === 'dark' || (mode === 'system' && darkMedia.matches);
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
    themeBtn.title = dark ? '切换亮色' : '切换暗色';
    // 同步 iOS 状态栏/地址栏配色（手动切换后不再跟随系统 media 查询）
    var tc = document.getElementById('themeColorMeta');
    if (tc) tc.setAttribute('content', dark ? '#0f1626' : '#eef2fb');
  }
  try{
    var savedTheme = localStorage.getItem(THEME_KEY);
    if (savedTheme === 'light' || savedTheme === 'dark'){
      applyTheme(savedTheme);
    } else {
      applyTheme('system');
      // 没手动设置过才跟随系统实时变化
      darkMedia.addEventListener('change', function(e){
        if (!localStorage.getItem(THEME_KEY)) applyTheme(e.matches ? 'dark' : 'light');
      });
    }
  }catch(e){ applyTheme('system'); }
  themeBtn.addEventListener('click', function(){
    var next = isDark() ? 'light' : 'dark';
    applyTheme(next);
    try{ localStorage.setItem(THEME_KEY, next); }catch(e){}
  });
  // 输入框内回车 = 点保存
  [maxTextInput, maxImageInput].forEach(function(el){
    el.addEventListener('keydown', function(e){
      if (e.key === 'Enter'){ e.preventDefault(); settingsSave.click(); }
    });
  });
  // ESC 关当前打开的弹窗（确认 > 设置 > 历史 > 二维码）；正在捕获快捷键时交给捕获逻辑
  document.addEventListener('keydown', function(e){
    if (e.key !== 'Escape') return;
    if (!confirmModal.hidden){ closeConfirm(false); return; }
    if (hkCapture) return;
    if (!addTextModal.hidden) closeAddText();
    else if (!settingsModal.hidden) closeSettings();
    else if (!historyModal.hidden) closeHistory();
    else if (!modal.hidden) closeModal();
  });

  document.getElementById('copyLinkBtn').addEventListener('click', function(){ copyText(state.shareUrl, '链接已复制'); });
  document.getElementById('qrCopy').addEventListener('click', function(){ copyText(state.shareUrl, '链接已复制'); });

  prevBtn.addEventListener('click', function(){ gotoPage(state.page - 1); });
  nextBtn.addEventListener('click', function(){ gotoPage(state.page + 1); });

  tabText.addEventListener('click', function(){ switchTab('text'); });
  tabImage.addEventListener('click', function(){ switchTab('image'); });
  tabFavorite.addEventListener('click', function(){ switchTab('favorite'); });

  // 页面右上角设置入口（仅 PC + 本机可见）：与托盘「设置」打开同一个弹窗
  if (settingsBtn) settingsBtn.addEventListener('click', openSettings);

  clearBtn.addEventListener('click', function(){
    var isImage = state.type === 'image';
    // 收藏是「这条我要留着」的显式标记，清空历史不会带走它（store.Clear 同语义）
    askConfirm({
      title: isImage ? '清空全部图片' : '清空全部文本',
      message: isImage ? '确定清空全部图片记录？此操作不可撤销。'
                       : '确定清空全部文本记录？收藏的文本会被保留。'
    }).then(function(ok){
      if (!ok) return;
      fetch('/api/clear?type=' + (isImage ? 'image' : 'text'), { method:'POST' }).then(function(r){
        if (r.ok){
          toast(isImage ? '已清空全部图片记录' : '已清空全部文本记录（收藏已保留）');
          state.page = 1; state.pages = 1; state.sig = ''; state.firstSig = '';
          loadView(true);
          return;
        }
        // 403 = 手机端无权限；5xx = 落盘失败（服务端 storageError 有具体原因）。
        r.json().then(function(j){
          toast(r.status === 403 ? '无权限清空' : (j && j.error) || '清空失败，请稍后重试');
        }).catch(function(){ toast('清空失败，请稍后重试'); });
      }).catch(function(){ toast('操作失败'); });
    });
  });

  // 收藏页「清空全部收藏」：按钮文案与文本/图片清空保持统一，
  // 但语义不同——只移除收藏标记、不删除记录（弹窗与 toast 里都写明）。
  // 与清空共用同一个居中确认弹窗；权限也在服务端按 /api/clear 同一套校验。
  unfavAllBtn.addEventListener('click', function(){
    if (state.favorites <= 0){ toast('暂无收藏'); return; }
    askConfirm({
      title: '清空全部收藏',
      message: '确定清空全部 ' + state.favorites + ' 条收藏？记录本身不会删除，只是移除收藏标记。'
    }).then(function(ok){
      if (!ok) return;
      unfavAllBtn.disabled = true;
      fetch('/api/unfavorite-all', { method: 'POST' }).then(readJSON).then(function(res){
        unfavAllBtn.disabled = false;
        if (!res.ok){ toast((res.j && res.j.error) || '操作失败'); return; }
        toast('已清空全部收藏（记录已保留）');
        state.page = 1; state.pages = 1; state.sig = ''; state.firstSig = '';
        loadView(true);
      }).catch(function(){
        unfavAllBtn.disabled = false;
        toast('操作失败');
      });
    });
  });

  // 保存设置：保留条数 + 开机自启一起提交，服务端立即生效
  settingsSave.addEventListener('click', function(){
    var t = parseInt(maxTextInput.value, 10);
    var i = parseInt(maxImageInput.value, 10);
    // 配置损坏时服务端可能把上限抬到 10–5000 / 5–1000 之外以保住存量，
    // 回填值会超出下面的范围；用户没改动过的字段原样提交、不做拦截，
    // 否则连「暂停记录」开关都保存不了。
    var tUnchanged = isFinite(t) && t === state.settings.maxText;
    var iUnchanged = isFinite(i) && i === state.settings.maxImage;
    if (!isFinite(t) || !isFinite(i) ||
        ((!tUnchanged && (t < 10 || t > 5000)) || (!iUnchanged && (i < 5 || i > 1000)))){
      toast('请输入有效范围：文本 10–5000，图片 5–1000');
      return;
    }
    var payload = { maxText: t, maxImage: i };
    var autostartChanged = state.canClear && state.settings.autostartSupported
      && autostartInput.checked !== state.settings.autostart;
    if (autostartChanged) payload.autostart = autostartInput.checked;
    if (pausedInput.checked !== state.settings.paused) payload.paused = pausedInput.checked;
    fetch('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    }).then(function(r){
      return r.json().then(function(j){ return { ok: r.ok, j: j }; });
    }).then(function(res){
      if (!res.ok){ toast(res.j.error || '保存失败'); return; }
      if (res.j.maxText !== undefined){
        state.settings.maxText = res.j.maxText;
        state.settings.maxImage = res.j.maxImage;
      }
      if (res.j.autostart !== undefined) state.settings.autostart = res.j.autostart;
      if (res.j.paused !== undefined) state.settings.paused = !!res.j.paused;
      else if (payload.paused !== undefined) state.settings.paused = payload.paused;
      var trimmed = (res.j.trimmedText || 0) + (res.j.trimmedImage || 0);
      var msg = trimmed > 0 ? ('已保存，清理 ' + trimmed + ' 条最早记录') : '已保存，立即生效';
      if (payload.autostart !== undefined){
        msg = autostartInput.checked ? '已保存，开机自动启动' : '已保存，已关闭开机自启';
      } else if (payload.paused !== undefined){
        msg = payload.paused ? '已保存，记录已暂停' : '已保存，已恢复记录';
      }
      toast(msg);
      closeSettings();
      state.page = 1; state.pages = 1; state.sig = ''; state.firstSig = '';
      loadView(true);
    }).catch(function(){ toast('操作失败'); });
  });

  if (mode === 'h5'){
    window.addEventListener('scroll', function(){
      if (state.loading || !state.hasMore) return;
      if (window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 240) loadMoreH5();
    }, { passive: true });
  }

  // 标签页隐藏 / 手机锁屏时暂停 2s 轮询，回到前台立即刷新一次再恢复。
  var pollTimer = null;
  function startPolling(){
    if (pollTimer) return;
    pollTimer = setInterval(poll, 2000);
  }
  function stopPolling(){
    if (!pollTimer) return;
    clearInterval(pollTimer); pollTimer = null;
  }
  document.addEventListener('visibilitychange', function(){
    if (document.hidden){ stopPolling(); }
    else { poll(); startPolling(); }
  });

  loadView(true);
  startPolling();
})();
