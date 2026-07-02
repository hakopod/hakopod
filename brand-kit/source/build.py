"""Hakopod vector identity. Authoring dependencies: fonttools, reportlab.

All deliverable SVGs contain geometry and outlined lettering only. They have no
scripts, remote resources, embedded raster images, or runtime font dependency.
Run from the kit folder with: python source/build.py --output .
"""
from __future__ import annotations
import argparse, copy, hashlib, html, json, math, re
from pathlib import Path
from xml.etree import ElementTree as ET
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.reportLabPen import ReportLabPen
from fontTools.svgLib.path.parser import parse_path
from reportlab.pdfgen import canvas
from reportlab.lib.colors import HexColor

parser=argparse.ArgumentParser()
parser.add_argument('--output', default='outputs/hakopod-brand-kit')
ROOT=Path(parser.parse_args().output).resolve()
for folder in ['logos/marks','logos/lockups','logos/wordmarks','concepts','marketing','icons','guide','brand','source']:
    (ROOT/folder).mkdir(parents=True,exist_ok=True)
INK='#111A17'; PAPER='#F4F5EE'; ION='#D8FF45'; FOREST='#2F5641'; FOG='#B8C4B9'; MUTED='#607065'; LINE='#D9DFD4'; WHITE='#FFFFFF'
PALETTE=[('Ink',INK),('Paper',PAPER),('Ion',ION),('Forest',FOREST),('Fog',FOG)]
FONT_PATH=ROOT/'fonts/SpaceGrotesk-Variable.ttf'
fonts={}; glyphs={}; renders=[]; pages=[]

def n(x):
    return str(round(x,3)).rstrip('0').rstrip('.') if isinstance(x,float) and not x.is_integer() else str(int(x))
def attrs(**kw): return ' '.join(f'{k.replace("_","-")}="{html.escape(str(v),quote=True)}"' for k,v in kw.items())
def rect(x,y,w,h,fill,rx=0,stroke=None,sw=1):
    a=dict(x=n(x),y=n(y),width=n(w),height=n(h),fill=fill)
    if rx:a['rx']=n(rx)
    if stroke:a.update(stroke=stroke,stroke_width=n(sw))
    return '<rect '+attrs(**a)+'/>'
def line(x1,y1,x2,y2,color=LINE,sw=1):return '<line '+attrs(x1=n(x1),y1=n(y1),x2=n(x2),y2=n(y2),stroke=color,stroke_width=n(sw))+'/>'
def path(d,fill,evenodd=False):return f'<path d="{d}" fill="{fill}"'+(' fill-rule="evenodd"' if evenodd else '')+'/>'
def group(content,transform=None,**attributes):
    if transform:attributes['transform']=transform
    return '<g '+attrs(**attributes)+'>'+content+'</g>'
def font(weight):
    if weight not in fonts:
        f=instantiateVariableFont(TTFont(FONT_PATH),{'wght':weight},inplace=True)
        fonts[weight]=(f, f.getGlyphSet(),f.getBestCmap(),f['hmtx'].metrics)
    return fonts[weight]
def glyph(char,weight):
    key=(weight,char)
    if key not in glyphs:
        f,g,cmap,metrics=font(weight)
        if ord(char) not in cmap:raise ValueError(f'Missing glyph {char!r}')
        name=cmap[ord(char)];pen=SVGPathPen(g);g[name].draw(pen)
        d=re.sub(r'-?\d+\.\d{4,}',lambda m:n(float(m.group())),pen.getCommands())
        glyphs[key]=(d,metrics[name][0])
    return glyphs[key]
def measure(content,size,weight=500,tracking=0):
    return sum(glyph(c,weight)[1]*size/1000 for c in content)+max(0,len(content)-1)*tracking
def text(content,x,y,size=24,color=INK,weight=500,tracking=0,align='left'):
    width=measure(content,size,weight,tracking)
    if align=='center':x-=width/2
    if align=='right':x-=width
    parts=[];cursor=0
    for c in content:
        d,advance=glyph(c,weight)
        if d:parts.append(f'<path d="{d}" transform="translate({n(cursor)} 0)"/>')
        cursor+=advance+tracking*1000/size
    return group(''.join(parts),f'translate({n(x)} {n(y)}) scale({n(size/1000)} {n(-size/1000)})',fill=color,aria_label=content)
def lines(content,x,y,size=24,leading=1.25,**kw):
    return ''.join(text(t,x,y+i*size*leading,size,**kw) for i,t in enumerate(content.split('\n')))
def svg(content,w,h,title,desc=''):
    return f'<svg xmlns="http://www.w3.org/2000/svg" width="{n(w)}" height="{n(h)}" viewBox="0 0 {n(w)} {n(h)}" role="img" aria-label="{html.escape(title,quote=True)}"><title>{html.escape(title)}</title><desc>{html.escape(desc or title)}</desc>{content}</svg>\n'
def save(name,content):
    dest=ROOT/name;dest.parent.mkdir(parents=True,exist_ok=True);dest.write_text(content)
def png_job(source,target,width=None,height=None):
    renders.append(dict(source=source,target=target,width=width,height=height))

CONCEPTS=[
 ('open-port','Open Port','The lead direction','Eight pods. One open core.'),
 ('bridge','Bridge','More architectural','Two banks, working together.'),
 ('shell','Shell','A developer instinct','A command line made tangible.'),
 ('signal','Signal','An asymmetric rhythm','Independent parts, shared direction.'),
 ('container','Container','A compact monogram','An H held inside its own pod.'),
 ('vector','Vector','A different perspective','The same system, set in motion.'),
]
def mark(kind='open-port',color=INK):
    if kind in ('open-port','signal'):
        boxes=[(42,6,12,36),(42,54,12,36),(18,18,12,24),(18,54,12,24),(66,18,12,24),(66,54,12,24),(6,42,12,12),(78,42,12,12)]
        if kind=='signal':boxes=[(42,6,12,36),(42,54,12,36),(18,24,12,18),(18,54,12,36),(66,6,12,36),(66,54,12,18),(6,42,12,12),(78,42,12,12)]
        return ''.join(rect(*r,color,1.5) for r in boxes)
    if kind=='bridge':
        return ''.join(rect(*r,color,1.5) for r in [(12,18,16,60),(68,18,16,60),(28,40,12,16),(56,40,12,16),(42,6,12,26),(42,64,12,26)])
    if kind=='shell':
        left=path('M30 18H18Q6 18 6 30V66Q6 78 18 78H30V66H18V30H30Z',color)
        return left+group(left,'translate(96 0) scale(-1 1)')+''.join(rect(42,y,12,h,color,1.5) for y,h in [(6,28),(42,12),(62,28)])
    if kind=='container':
        return path('M24 6H72Q90 6 90 24V72Q90 90 72 90H24Q6 90 6 72V24Q6 6 24 6Z M28 22H40V42H56V22H68V74H56V54H40V74H28Z',color,True)
    if kind=='vector':return group(mark('open-port',color),'translate(48 48) rotate(45) scale(0.84) translate(-48 -48)')
    raise ValueError(kind)
def placed_mark(x,y,size,color=INK,kind='open-port'):
    return group(mark(kind,color),f'translate({n(x)} {n(y)}) scale({n(size/96)})')
def micro(color=INK):
    return ''.join(rect(*r,color) for r in [(7,1,2,6),(7,9,2,6),(3,3,2,4),(3,9,2,4),(11,3,2,4),(11,9,2,4),(1,7,2,2),(13,7,2,2)])
def wordmark(color=INK):
    return text('hakopod',0,76,86,color,600,-2.4)
WORD_WIDTH=measure('hakopod',86,600,-2.4)
LOCKUP_WIDTH=122+WORD_WIDTH+6
def lockup(x,y,width,color=INK):
    return group(placed_mark(2,2,96,color)+group(wordmark(color),'translate(122 0)'),f'translate({n(x)} {n(y)}) scale({n(width/LOCKUP_WIDTH)})')
def fitted_wordmark(x,y,width,color=INK):return group(wordmark(color),f'translate({n(x)} {n(y)}) scale({n(width/WORD_WIDTH)})')

# Production marks and lockups.
for name,color in [('ink',INK),('paper',PAPER),('ion',ION),('black','#000000'),('white','#FFFFFF'),('currentcolor','currentColor')]:
    save(f'logos/marks/hakopod-mark-{name}.svg',svg(mark(color=color),96,96,'Hakopod Open Port mark','Eight modular blocks surrounding an open center.'))
    save(f'logos/lockups/hakopod-horizontal-{name}.svg',svg(lockup(0,0,LOCKUP_WIDTH,color),LOCKUP_WIDTH,104,'Hakopod horizontal logo','Open Port symbol with the outlined lowercase hakopod wordmark.'))
    save(f'logos/wordmarks/hakopod-wordmark-{name}.svg',svg(group(wordmark(color),'translate(4 0)'),WORD_WIDTH+8,104,'Hakopod wordmark'))
    if name!='currentcolor':
        stack=placed_mark(104,4,112,color)+fitted_wordmark(32,138,256,color)
        save(f'logos/lockups/hakopod-stacked-{name}.svg',svg(stack,320,210,'Hakopod stacked logo'))
        png_job(f'logos/marks/hakopod-mark-{name}.svg',f'logos/marks/hakopod-mark-{name}-512.png',512,512)
        png_job(f'logos/lockups/hakopod-horizontal-{name}.svg',f'logos/lockups/hakopod-horizontal-{name}-1200.png',1200,round(1200*104/LOCKUP_WIDTH))
save('logos/marks/hakopod-micro.svg',svg(micro(),16,16,'Hakopod pixel-grid micro mark','Use this optical version below 24 pixels.'))
for index,(key,label,_,desc) in enumerate(CONCEPTS,1):
    save(f'concepts/{index:02d}-{key}.svg',svg(mark(key),96,96,f'Hakopod concept {index:02d}: {label}',desc))

# Symbol tile, favicon, mobile, and installable-app assets.
tile=rect(0,0,512,512,INK)+placed_mark(82,82,348,ION)
save('icons/hakopod-app-icon.svg',svg(tile,512,512,'Hakopod application icon'))
favicon=rect(0,0,16,16,INK,3)+micro(ION)
save('icons/favicon.svg',svg(favicon,16,16,'Hakopod favicon'))
for size in [16,32,48,64]:png_job('icons/favicon.svg',f'icons/favicon-{size}.png',size,size)
for size in [180,192,512]:png_job('icons/hakopod-app-icon.svg',f'icons/icon-{size}.png',size,size)
save('icons/site.webmanifest',json.dumps({'name':'Hakopod','short_name':'Hakopod','icons':[{'src':'icon-192.png','sizes':'192x192','type':'image/png'},{'src':'icon-512.png','sizes':'512x512','type':'image/png'}],'theme_color':INK,'background_color':PAPER,'display':'standalone'},indent=2)+'\n')

def motif(x,y,size,color=FOREST,rows=4,cols=6,step=110):
    return ''.join(placed_mark(x+c*step,y+r*step,size,color) for r in range(rows) for c in range(cols))

def hero(w=1200,h=630):
    split=w*0.61
    body=rect(0,0,w,h,INK)+rect(split,0,w-split,h,ION)
    body+=lockup(54,42,270,PAPER)
    body+=lines('Your apps.\nYour rules.',54,260,78,1.1,color=PAPER,weight=500,tracking=-2.2)
    body+=lines('Deploy on infrastructure\nyou own.',58,466,25,1.28,color=FOG,weight=400,tracking=-0.25)
    body+=text('THE SELF-HOSTED APPLICATION PLATFORM',58,h-39,12,PAPER,600,1.2)
    ms=(w-split)*0.73
    body+=placed_mark(split+(w-split-ms)/2,(h-ms)/2-7,ms,INK)
    body+=text('OPEN BY DESIGN',split+(w-split)/2,h-41,12,INK,600,1.6,align='center')
    return body
save('marketing/open-graph-1200x630.svg',svg(hero(),1200,630,'Hakopod: Your apps. Your rules.','Social sharing card for the self-hosted application platform.'))
png_job('marketing/open-graph-1200x630.svg','marketing/open-graph-1200x630.png',1200,630)

square=rect(0,0,1080,1080,INK)+lockup(64,53,333,PAPER)
square+=placed_mark(256,229,568,ION)
square+=lines('Run it on\nyour terms.',64,892,64,1.05,color=PAPER,weight=500,tracking=-1.4)
square+=text('HAKOPOD / SELF-HOSTED',1016,1014,13,FOG,500,1.1,align='right')
save('marketing/social-square-1080.svg',svg(square,1080,1080,'Hakopod social square: Run it on your terms.'))
png_job('marketing/social-square-1080.svg','marketing/social-square-1080.png',1080,1080)

banner=rect(0,0,1500,500,PAPER)+rect(1020,0,480,500,INK)
banner+=lockup(58,39,282,INK)+lines('Build freely.\nRun independently.',62,257,72,1.05,color=INK,weight=500,tracking=-2)
banner+=placed_mark(1107,97,306,ION)+text('YOUR INFRASTRUCTURE. IN YOUR HANDS.',65,445,14,FOREST,600,0.8)
save('marketing/profile-banner-1500x500.svg',svg(banner,1500,500,'Hakopod profile banner'))
png_job('marketing/profile-banner-1500x500.svg','marketing/profile-banner-1500x500.png',1500,500)

poster=rect(0,0,1080,1350,ION)+lockup(64,42,333,INK)
poster+=lines('Make\nyourself\nat home.',60,360,124,1.0,color=INK,weight=500,tracking=-4)
poster+=placed_mark(604,700,422,INK)+lines('A home for your apps,\non infrastructure you own.',66,1037,35,1.3,color=INK,weight=400,tracking=-.45)
poster+=line(64,1228,1016,1228,INK,2)+text('HAKOPOD',64,1290,20,INK,600,1.1)+text('OPEN BY DESIGN',1016,1290,16,INK,600,1.2,align='right')
save('marketing/launch-poster-1080x1350.svg',svg(poster,1080,1350,'Hakopod launch poster: Make yourself at home.'))
png_job('marketing/launch-poster-1080x1350.svg','marketing/launch-poster-1080x1350.png',1080,1350)

badge=rect(0,0,288,56,INK,10)+placed_mark(12,9,38,ION)+text('built with hakopod',64,36,19,PAPER,500,-.15)
save('marketing/built-with-hakopod.svg',svg(badge,288,56,'Built with Hakopod badge'))
png_job('marketing/built-with-hakopod.svg','marketing/built-with-hakopod.png',576,112)
pattern=rect(0,0,1200,800,INK)+motif(-24,-24,96,FOREST,8,12,110)
save('marketing/pod-pattern.svg',svg(pattern,1200,800,'Hakopod modular pod pattern'))

def footer(page,total=6,dark=False):
    c=FOG if dark else MUTED
    return text('HAKOPOD / IDENTITY SYSTEM 01',64,861,12,c,500,1)+text(f'{page:02d} / {total:02d}',1376,861,12,c,500,1,align='right')
def heading(num,kicker,title,sub=None):
    s=rect(0,0,1440,900,PAPER)+text(kicker.upper(),64,58,13,FOREST,600,1.1)+text(title,60,133,56,INK,500,-1.6)
    if sub:s+=text(sub,64,177,20,MUTED,400,-.15)
    return s+footer(num)

# Six-page vector brand guide.
p1=rect(0,0,1440,900,INK)+rect(884,0,556,900,ION)+lockup(64,49,356,PAPER)
p1+=lines('Your apps.\nYour rules.',59,368,100,1.04,color=PAPER,weight=500,tracking=-3.2)
p1+=lines('A home for your apps,\non infrastructure you own.',65,629,30,1.27,color=FOG,weight=400,tracking=-.2)
p1+=text('LOGO + MARKETING IDENTITY',66,806,14,PAPER,600,1.25)
p1+=placed_mark(954,252,416,INK)+text('OPEN PORT / LEAD DIRECTION',1162,804,12,INK,600,1.35,align='center')
p1+=text('01 / 06',1376,861,12,INK,500,1,align='right')
pages.append(('01-the-identity',p1))

p2=heading(2,'01 / Exploration','Six ways to open the core.','All six directions are supplied as editable, single-color SVGs. Open Port leads the system.')
for i,(key,label,qualifier,desc) in enumerate(CONCEPTS):
    x=64+(i%3)*442;y=216+(i//3)*288
    bg=INK if i==0 else WHITE;fg=ION if i==0 else INK;fg2=FOG if i==0 else MUTED
    p2+=rect(x,y,428,267,bg,14)
    p2+=text(f'{i+1:02d} / {label}',x+24,y+36,18,PAPER if i==0 else INK,600,-.2)
    p2+=placed_mark(x+151,y+60,126,fg,key)
    p2+=text(qualifier,x+24,y+220,15,PAPER if i==0 else FOREST,500)
    p2+=text(desc,x+24,y+245,14,fg2,400,-.1)
pages.append(('02-logo-directions',p2))

p3=heading(3,'02 / Logo system','One mark. A useful family.','Use the horizontal lockup first. Use the symbol when the product name is already clear.')
p3+=rect(64,216,834,238,WHITE,14)+lockup(142,265,676,INK)
p3+=text('PRIMARY / HORIZONTAL',89,427,12,MUTED,600,1)
p3+=rect(920,216,218,238,INK,14)+placed_mark(957,249,144,ION)+text('DARK SURFACES',943,427,11,PAPER,600,.7)
p3+=rect(1158,216,218,238,ION,14)+placed_mark(1195,249,144,INK)+text('ACCENT SURFACES',1180,427,11,INK,600,.7)
p3+=rect(64,477,404,306,WHITE,14)+placed_mark(203,496,124,INK)+fitted_wordmark(112,626,305,INK)+text('STACKED LOCKUP',88,755,12,MUTED,600,1)
p3+=rect(490,477,886,306,WHITE,14)+text('SMALL, WITHOUT DISAPPEARING.',516,516,14,FOREST,600,1)
for size,x in [(16,536),(24,620),(32,722),(48,844),(72,1001),(104,1188)]:
    if size<24:p3+=group(micro(),f'translate({x} 580) scale({size/16})')
    else:p3+=placed_mark(x,580,size,INK)
    p3+=text(f'{size}px',x,715,14,MUTED,400)
p3+=text('Pixel-aligned micro SVG below 24px. Standard mark at 24px and above.',516,749,16,MUTED,400,-.12)
pages.append(('03-logo-system',p3))

def luminance(h):
    rgb=[int(h[i:i+2],16)/255 for i in (1,3,5)]
    v=[x/12.92 if x<=.04045 else ((x+.055)/1.055)**2.4 for x in rgb]
    return .2126*v[0]+.7152*v[1]+.0722*v[2]
def contrast(a,b):
    values=sorted([luminance(a),luminance(b)])
    return (values[1]+.05)/(values[0]+.05)

p4=heading(4,'03 / Color + typography','Quiet foundation. Bright intent.','Ink and Paper do the everyday work. Ion is a deliberate highlight, paired with dark text.')
for i,(name,color) in enumerate(PALETTE):
    x=64+i*265;fg=PAPER if name in ['Ink','Forest'] else INK
    p4+=rect(x,217,252,174,color,12,LINE if name=='Paper' else None)
    p4+=text(name,x+20,257,23,fg,500,-.4)+text(color,x+20,362,17,fg,400,.3)
p4+=text('SPACE GROTESK',64,456,14,FOREST,600,1.4)
p4+=text('Open. Capable. Yours.',61,540,64,INK,500,-2)
p4+=text('Aa Bb Cc Dd Ee Ff Gg Hh 0123456789',64,601,28,INK,400,-.3)
p4+=text('Regular 400 / body      Medium 500 / headlines      Semibold 600 / identity',64,648,17,MUTED,400,-.1)
p4+=lines('Sentence case. Clear verbs. Technical detail when it helps.\nTalk about ownership, visibility, and getting useful work done.',64,721,21,1.45,color=INK,weight=400)
p4+=rect(1000,450,376,306,INK,14)+text('CONTRAST, CHECKED',1027,488,13,ION,600,1)
p4+=text(f'Paper on Ink  {contrast(PAPER,INK):.1f}:1',1027,549,22,PAPER,500,-.3)
p4+=text(f'Ink on Ion  {contrast(INK,ION):.1f}:1',1027,598,22,ION,500,-.3)
p4+=lines('Use Forest for accent text on Paper.\nDo not set Ion text on Paper.',1027,675,16,1.5,color=FOG,weight=400)
pages.append(('04-colors-type',p4))

p5=heading(5,'04 / In the world','A system made to ship.','Editable campaign artwork for sharing, launches, profiles, and product attribution.')
p5+=group(hero(),'translate(64 214) scale(.68)')
p5+=group(square,'translate(920 214) scale(.422)')
p5+=text('OPEN GRAPH / 1200 x 630',64,676,13,FOREST,600,1)
p5+=text('SOCIAL SQUARE / 1080 x 1080',920,702,13,FOREST,600,1)
p5+=lines('Also included: profile banner, launch poster,\nrepeating pod pattern, and a Built with Hakopod badge.',64,737,22,1.35,color=INK,weight=400,tracking=-.2)
p5+=group(badge,'translate(1027 751) scale(1.12)')
pages.append(('05-marketing',p5))

p6=heading(6,'05 / Practical rules','Let the open space stay open.','The space between the modules is part of the mark. Preserve it with the same care as the blocks.')
p6+=rect(64,217,548,520,WHITE,14)
for x in range(106,584,32):p6+=line(x,245,x,699,'#E8ECE3',.8)
for y in range(251,706,32):p6+=line(92,y,584,y,'#E8ECE3',.8)
p6+=rect(176,315,324,324,'none',0,FOREST,1.5)+placed_mark(194,333,288,INK)
p6+=line(176,289,212,289,FOREST,1.5)+line(176,283,176,295,FOREST,1.5)+line(212,283,212,295,FOREST,1.5)
p6+=text('1X',194,274,12,FOREST,600,1,align='center')
p6+=text('X = the width of one module',94,766,19,MUTED,400,-.1)
p6+=text('USE',670,251,14,FOREST,600,1.3)
for j,(title,desc) in enumerate([
 ('One solid color.','Choose Ink, Paper, Ion, or a true one-color reproduction.'),
 ('Consistent proportions.','Scale the entire asset. Keep all eight modules together.'),
 ('Enough room.','Leave at least one module of clear space on every side.'),
 ('The right small-size mark.','Use the micro version at 16px. Use the master from 24px.'),
 ('A clear wordmark.','Use supplied outlined lockups; do not retype the logo.'),
 ]):
    y=307+j*83
    p6+=text(title,670,y,23,INK,500,-.25)+text(desc,670,y+31,17,MUTED,400,-.15)
p6+=rect(659,750,717,55,INK,10)+text('Avoid stretching, gradients, outlines, and multicolor modules.',680,785,18,PAPER,400,-.15)
pages.append(('06-logo-use',p6))

for filename,body in pages:
    save(f'guide/{filename}.svg',svg(body,1440,900,filename.replace('-',' ').title()))

# A single contact sheet for the conversation and quick hand-off.
board=group(hero(),'scale(1.2)')
for i,(name,color) in enumerate(PALETTE):
    x=i*288;y=756;fg=PAPER if name in ['Ink','Forest'] else INK
    board+=rect(x,y,288,90,color)+text(name,x+24,y+33,16,fg,500)+text(color,x+24,y+65,14,fg,400,.3)
board+=rect(0,846,1440,346,PAPER)+text('One core. Six directions.',32,895,28,INK,500,-.65)
for i,(key,label,_,desc) in enumerate(CONCEPTS):
    x=32+i*232;bg=INK if i==0 else WHITE;fg=ION if i==0 else INK
    board+=rect(x,921,216,236,bg,12)+placed_mark(x+52,942,112,fg,key)
    board+=text(f'{i+1:02d} / {label}',x+18,1101,18,PAPER if i==0 else INK,500,-.2)
    board+=text('LEAD DIRECTION' if i==0 else 'ALTERNATE STUDY',x+18,1132,10,FOG if i==0 else MUTED,600,1)
save('guide/identity-board.svg',svg(board,1440,1192,'Hakopod identity: Open Port and five alternate directions'))
png_job('guide/identity-board.svg','preview.png',1440,1192)
png_job('guide/02-logo-directions.svg','guide/logo-directions.png',1440,900)

# Tiny SVG painter used only to preserve all vectors in the PDF guide.
class CanvasPen(ReportLabPen):
    def _closePath(self): self.path.close()

def draw_svg(c,xml):
    root=ET.fromstring(xml)
    def paint(el, inherited=None):
        style=dict(inherited or {'fill':INK,'stroke':'none','stroke-width':'1'})
        for k in ['fill','stroke','stroke-width','fill-rule','opacity']:
            if k in el.attrib:style[k]=el.attrib[k]
        tag=el.tag.rsplit('}',1)[-1]
        if tag in ['title','desc','defs']:return
        c.saveState()
        for op,raw in re.findall(r'(translate|scale|rotate|matrix)\(([^)]*)\)',el.get('transform','')):
            a=[float(x) for x in re.split(r'[ ,]+',raw.strip())]
            if op=='translate':c.translate(a[0],a[1] if len(a)>1 else 0)
            elif op=='scale':c.scale(a[0],a[1] if len(a)>1 else a[0])
            elif op=='matrix':c.transform(*a)
            elif op=='rotate':
                if len(a)==3:c.translate(a[1],a[2])
                c.rotate(a[0])
                if len(a)==3:c.translate(-a[1],-a[2])
        fill=style.get('fill','none')!='none';stroke=style.get('stroke','none')!='none'
        if fill:c.setFillColor(HexColor(style['fill']))
        if stroke:c.setStrokeColor(HexColor(style['stroke']));c.setLineWidth(float(style.get('stroke-width',1)))
        if 'opacity' in style:c.setFillAlpha(float(style['opacity']));c.setStrokeAlpha(float(style['opacity']))
        if tag=='rect':
            x,y,w,h=(float(el.get(k,0)) for k in ['x','y','width','height']);r=float(el.get('rx',0))
            if r:c.roundRect(x,y,w,h,r,stroke=int(stroke),fill=int(fill))
            else:c.rect(x,y,w,h,stroke=int(stroke),fill=int(fill))
        elif tag=='line':c.line(*(float(el.get(k,0)) for k in ['x1','y1','x2','y2']))
        elif tag=='path':
            pen=CanvasPen(None,c.beginPath());parse_path(el.get('d',''),pen)
            c.drawPath(pen.path,stroke=int(stroke),fill=int(fill),fillMode=0 if style.get('fill-rule')=='evenodd' else 1)
        elif tag in ['svg','g']:
            for child in el:paint(child,style)
        else:raise ValueError(f'Unhandled vector element {tag}')
        c.restoreState()
    paint(root)

pdf=canvas.Canvas(str(ROOT/'guide/hakopod-brand-guide.pdf'),pagesize=(720,450),pageCompression=1)
pdf.setTitle('Hakopod - Logo and Marketing Identity Kit')
pdf.setAuthor('Hakopod')
pdf.setSubject('Six logo directions, Open Port identity, color, typography, marketing, and use guidance')
for filename,_ in pages:
    pdf.saveState();pdf.translate(0,450);pdf.scale(.5,-.5)
    draw_svg(pdf,(ROOT/f'guide/{filename}.svg').read_text())
    pdf.restoreState();pdf.showPage()
pdf.save()

tokens={'name':'Hakopod / Open Port','colors':{k.lower():v for k,v in PALETTE},'semantic':{'background':PAPER,'foreground':INK,'accent':ION,'accent_foreground':INK,'text_on_light_accent':FOREST,'muted_text':MUTED,'border':LINE},'typography':{'family':'Space Grotesk','body_weight':400,'heading_weight':500,'logo_weight':600,'fallback':'system-ui, sans-serif'},'logo':{'master_viewbox':[0,0,96,96],'module':12,'corner_radius':1.5,'clear_space':12,'minimum_standard_px':24,'minimum_micro_px':16,'minimum_horizontal_px':140},'contrast':{'paper_on_ink':round(contrast(PAPER,INK),2),'ink_on_ion':round(contrast(INK,ION),2),'forest_on_paper':round(contrast(FOREST,PAPER),2),'muted_on_paper':round(contrast(MUTED,PAPER),2)}}
save('brand/tokens.json',json.dumps(tokens,indent=2)+'\n')
save('brand/tokens.css','/* Hakopod / Open Port. Logo geometry is independent of dashboard accent preferences. */\n:root {\n'+''.join(f'  --hakopod-{k.lower()}: {v};\n' for k,v in PALETTE)+f'  --hakopod-muted: {MUTED};\n  --hakopod-border: {LINE};\n  --hakopod-font: "Space Grotesk", system-ui, sans-serif;\n'+'}\n')
save('source/render-jobs.json',json.dumps(renders,indent=2)+'\n')
print(json.dumps({'svg_count':len(list(ROOT.rglob('*.svg'))),'pdf_pages':len(pages),'mark_bytes':(ROOT/'logos/marks/hakopod-mark-ink.svg').stat().st_size,'contrast':tokens['contrast'],'output':str(ROOT)},indent=2))
