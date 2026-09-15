const search=document.querySelector('#search'), severity=document.querySelector('#severity'), cards=[...document.querySelectorAll('.finding')];
function filter(){const q=search.value.trim().toLowerCase(),s=severity.value;let n=0;for(const c of cards){const show=(!s||c.dataset.severity===s)&&(!q||c.textContent.toLowerCase().includes(q));c.hidden=!show;if(show)n++}document.querySelector('#result-count').textContent=`${n} of ${cards.length} findings`;document.querySelector('#empty').hidden=n!==0;}
search.addEventListener('input',filter);severity.addEventListener('change',filter);
function revealHash(){const el=document.getElementById(location.hash.slice(1));if(el?.classList.contains('finding')&&el.hidden){search.value='';severity.value='';filter();el.scrollIntoView();}}
window.addEventListener('hashchange',revealHash);revealHash();
document.querySelector('#print').addEventListener('click',()=>window.print());
let detailStates=[];window.addEventListener('beforeprint',()=>{detailStates=[...document.querySelectorAll('details')].map(d=>[d,d.open]);for(const [d]of detailStates)d.open=true;document.body.classList.add('printing');});
window.addEventListener('afterprint',()=>{for(const[d,o]of detailStates)d.open=o;document.body.classList.remove('printing');});
