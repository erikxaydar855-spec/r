(() => {
  function install() {
    if (document.getElementById('itube-pass-eye-style')) return;
    const style=document.createElement('style');style.id='itube-pass-eye-style';
    style.textContent='.itube-pass-wrap{position:relative;display:block;width:100%}.itube-pass-wrap>input{padding-right:48px!important}.itube-pass-eye{position:absolute;right:7px;top:50%;transform:translateY(-50%);width:37px;height:37px;display:grid;place-items:center;border:0;border-radius:10px;background:rgba(116,141,179,.10);color:#30476a;font-size:18px;cursor:pointer;z-index:3}.itube-pass-eye:hover{background:rgba(59,130,246,.16)}';
    document.head.appendChild(style);
    document.querySelectorAll('input[type="password"]').forEach(input=>{
      if(input.dataset.itubeEye==='1')return;
      input.dataset.itubeEye='1';
      const wrap=document.createElement('span');wrap.className='itube-pass-wrap';
      input.parentNode.insertBefore(wrap,input);wrap.appendChild(input);
      const button=document.createElement('button');button.type='button';button.className='itube-pass-eye';button.textContent='👁';button.setAttribute('aria-label','Parolni ko‘rsatish');button.setAttribute('aria-pressed','false');
      button.addEventListener('click',()=>{let shown=input.type==='password';input.type=shown?'text':'password';button.setAttribute('aria-pressed',String(shown));button.setAttribute('aria-label',shown?'Parolni yashirish':'Parolni ko‘rsatish');});wrap.appendChild(button);
    });
  }
  if(document.readyState==='loading')document.addEventListener('DOMContentLoaded',install);else install();
})();
