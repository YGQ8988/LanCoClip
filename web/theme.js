/* 主题：默认跟随系统；用户手动切换后记在 localStorage，之后按用户设置展示。
   这段内联脚本放在 <head> 里、样式表之前，确保首帧就是正确主题、不闪白/闪黑 */
(function(){
  try{
    var m = localStorage.getItem('lanoclip-theme') || 'system';
    var dark = m === 'dark' || (m === 'system' && matchMedia('(prefers-color-scheme: dark)').matches);
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
    // iOS 顶部状态栏/地址栏配色跟随实际主题（手动切换时也必须跟着变）
    var tc = document.getElementById('themeColorMeta');
    if (tc) tc.setAttribute('content', dark ? '#0f1626' : '#eef2fb');
  }catch(e){ /* 隐私模式等场景下 localStorage 不可用，退回默认亮色 */ }
})();
