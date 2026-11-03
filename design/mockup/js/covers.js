/* Stand-in covers. Real items use the fetched og:image, a Worker screenshot or the uploaded image. */
let uid=0;
function rng(seed){let s=0;for(const ch of seed)s=(s*31+ch.charCodeAt(0))>>>0;return()=>{s=(s*1664525+1013904223)>>>0;return s/4294967296}}
function rgb(h){const n=parseInt(h.slice(1),16);return[(n>>16)&255,(n>>8)&255,n&255].map(v=>v/255)}
function cover(it){
  const cv=it.cv,W=240,H=Math.round(W/cv.ar),r=rng(it.id),c=cv.c,g='g'+(++uid);
  const grad=`<defs><linearGradient id="${g}" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="${c[0]}"/><stop offset="1" stop-color="${c[1]}"/></linearGradient></defs><rect width="${W}" height="${H}" fill="url(#${g})"/>`;
  let b='';
  if(cv.t==='grad'){
    for(let k=0;k<4;k++)b+=`<circle cx="${r()*W}" cy="${r()*H}" r="${H*(.18+r()*.4)}" fill="${c[2]}" opacity="${.07+r()*.17}"/>`;
    b+=`<path d="M0 ${H*.74} Q ${W*.25} ${H*.6} ${W*.5} ${H*.74} T ${W} ${H*.7} V${H} H0Z" fill="#000" opacity=".22"/>`;
  }else if(cv.t==='prod'){
    b=`<defs><radialGradient id="${g}r" cx=".38" cy=".34" r=".7"><stop offset="0" stop-color="#9aa9c4"/><stop offset=".55" stop-color="#4f5a70"/><stop offset="1" stop-color="#232834"/></radialGradient></defs><ellipse cx="${W/2}" cy="${H*.86}" rx="${W*.36}" ry="${H*.06}" fill="#000" opacity=".35"/><rect x="${W*.2}" y="${H*.52}" width="${W*.6}" height="${H*.3}" rx="${W*.12}" fill="#2d323d"/><circle cx="${W/2}" cy="${H*.46}" r="${W*.24}" fill="url(#${g}r)"/>`;
  }else if(cv.t==='page'){
    b=`<rect width="${W}" height="${H}" fill="${c[0]}"/><rect x="26" y="26" width="${W-52}" height="9" fill="#2b2b2b"/><rect x="62" y="42" width="${W-124}" height="5" fill="#777"/>`;
    for(let k=0;k<17;k++)b+=`<rect x="${k%2?26:26}" y="${74+k*9.5}" width="${(W-60)/2-(k===8?18:0)}" height="3" fill="#9a968a"/><rect x="${W/2+4}" y="${74+k*9.5}" width="${(W-60)/2}" height="3" fill="#9a968a"/>`;
    b+=`<rect x="30" y="${H*.52}" width="${(W-60)/2-6}" height="${H*.18}" fill="${c[2]}" opacity=".35"/>`;
  }else if(cv.t==='map'){
    b=grad;for(let k=0;k<9;k++){b+=`<path d="M${r()*W} 0 L${r()*W} ${H}" stroke="${c[1]}" stroke-width="${2+r()*3}" opacity=".7"/><path d="M0 ${r()*H} L${W} ${r()*H}" stroke="${c[1]}" stroke-width="${2+r()*3}" opacity=".7"/>`}
    b+=`<path d="M0 ${H*.8} C ${W*.3} ${H*.5} ${W*.6} ${H*.95} ${W} ${H*.4}" stroke="#2f7088" stroke-width="12" fill="none" opacity=".75"/><circle cx="${W*.56}" cy="${H*.42}" r="18" fill="${c[2]}" opacity=".25"/><circle cx="${W*.56}" cy="${H*.42}" r="7" fill="${c[2]}"/><circle cx="${W*.56}" cy="${H*.42}" r="2.5" fill="#fff"/>`;
    return svg(W,H,b);
  }else if(cv.t==='book'){
    b=`<rect x="12" y="12" width="${W-24}" height="${H-24}" fill="none" stroke="${c[2]}" stroke-width="1" opacity=".7"/>`+cv.tt.map((t,k)=>`<text x="${W/2}" y="${H*.34+k*26}" text-anchor="middle" font-family="Newsreader,serif" font-size="${k===1?15:23}" letter-spacing="2" fill="${c[2]}">${t}</text>`).join('')+`<text x="${W/2}" y="${H-34}" text-anchor="middle" font-family="Instrument Sans,sans-serif" font-size="8" letter-spacing="2.4" fill="${c[2]}" opacity=".8">${cv.au}</text>`;
  }else if(cv.t==='poster'){
    b=`<circle cx="${W/2}" cy="${H*.4}" r="${W*.27}" fill="${c[2]}" opacity=".92"/><path d="M0 ${H*.64} L${W*.28} ${H*.5} L${W*.5} ${H*.62} L${W*.74} ${H*.46} L${W} ${H*.62} V${H} H0Z" fill="#1c0f26" opacity=".85"/>`+cv.tt.map((t,k)=>`<text x="${W/2}" y="${H*.8+k*20}" text-anchor="middle" font-family="Newsreader,serif" font-size="19" letter-spacing="4" fill="#ffe9c8">${t}</text>`).join('');
  }else if(cv.t==='wave'){
    for(let k=0;k<22;k++){const h=14+r()*H*.5*(1-Math.abs(k-11)/14);b+=`<rect x="${14+k*10}" y="${H/2-h/2}" width="5" height="${h}" rx="2.5" fill="${c[2]}" opacity="${.45+r()*.5}"/>`}
  }else if(cv.t==='tex'){
    const[R,G,B]=rgb(c[1]);
    b=`<defs><filter id="${g}f" x="0" y="0" width="100%" height="100%"><feTurbulence type="fractalNoise" baseFrequency=".035 .06" numOctaves="4" seed="7"/><feColorMatrix values="0 0 0 0 ${R} 0 0 0 0 ${G} 0 0 0 0 ${B} 0 0 0 1.7 -.45"/></filter></defs><rect width="${W}" height="${H}" fill="${c[2]}"/><rect width="${W}" height="${H}" fill="${c[0]}" opacity=".55"/><rect width="${W}" height="${H}" filter="url(#${g}f)"/>`;
    return svg(W,H,b);
  }
  return svg(W,H,(cv.t==='page'||cv.t==='prod')?b:grad+b);
}
function svg(W,H,inner){return `<svg class="cover" viewBox="0 0 ${W} ${H}" role="img" aria-label="Sample cover">${inner}</svg>`}
