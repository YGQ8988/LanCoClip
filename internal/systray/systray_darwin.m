#import <Cocoa/Cocoa.h>
#include "systray.h"

#if __MAC_OS_X_VERSION_MIN_REQUIRED < 101400

    #ifndef NSControlStateValueOff
      #define NSControlStateValueOff NSOffState
    #endif

    #ifndef NSControlStateValueOn
      #define NSControlStateValueOn NSOnState
    #endif

#endif

@interface MenuItem : NSObject
{
  @public
    NSNumber* menuId;
    NSNumber* parentMenuId;
    NSString* title;
    NSString* tooltip;
    short disabled;
    short checked;
}
-(id) initWithId: (int)theMenuId
withParentMenuId: (int)theParentMenuId
       withTitle: (const char*)theTitle
     withTooltip: (const char*)theTooltip
    withDisabled: (short)theDisabled
     withChecked: (short)theChecked;
     @end
     @implementation MenuItem
     -(id) initWithId: (int)theMenuId
     withParentMenuId: (int)theParentMenuId
            withTitle: (const char*)theTitle
          withTooltip: (const char*)theTooltip
         withDisabled: (short)theDisabled
          withChecked: (short)theChecked
{
  menuId = [NSNumber numberWithInt:theMenuId];
  parentMenuId = [NSNumber numberWithInt:theParentMenuId];
  title = [[NSString alloc] initWithCString:theTitle
                                   encoding:NSUTF8StringEncoding];
  tooltip = [[NSString alloc] initWithCString:theTooltip
                                     encoding:NSUTF8StringEncoding];
  disabled = theDisabled;
  checked = theChecked;
  return self;
}
@end

@interface AppDelegate: NSObject <NSApplicationDelegate, NSMenuDelegate>
  - (void) add_or_update_menu_item:(MenuItem*) item;
  - (IBAction)menuHandler:(id)sender;
  - (void)menu:(NSMenu *)menu willHighlightItem:(NSMenuItem *)item;
  - (void)menuDidClose:(NSMenu *)menu;
  @property (assign) IBOutlet NSWindow *window;
  @end

  @implementation AppDelegate
{
  NSStatusItem *statusItem;
  NSMenu *menu;
  NSCondition* cond;
}

@synthesize window = _window;

- (void)applicationDidFinishLaunching:(NSNotification *)aNotification
{
  self->statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
  self->menu = [[NSMenu alloc] init];
  [self->menu setAutoenablesItems: FALSE];
  // 菜单高亮变化（鼠标移到某项上）通过 NSMenuDelegate 上报，供 hover 类交互使用
  [self->menu setDelegate:self];
  [self->statusItem setMenu:self->menu];
  systray_ready();
}

- (void)applicationWillTerminate:(NSNotification *)aNotification
{
  systray_on_exit();
}

- (void)setIcon:(NSImage *)image {
  statusItem.button.image = image;
  [self updateTitleButtonStyle];
}

- (void)setTitle:(NSString *)title {
  statusItem.button.title = title;
  [self updateTitleButtonStyle];
}

-(void)updateTitleButtonStyle {
  if (statusItem.button.image != nil) {
    if ([statusItem.button.title length] == 0) {
      statusItem.button.imagePosition = NSImageOnly;
    } else {
      statusItem.button.imagePosition = NSImageLeft;
    }
  } else {
    statusItem.button.imagePosition = NSNoImage;
  }
}


- (void)setTooltip:(NSString *)tooltip {
  statusItem.button.toolTip = tooltip;
}

- (IBAction)menuHandler:(id)sender
{
  NSNumber* menuId = [sender representedObject];
  systray_menu_item_selected(menuId.intValue);
}

// systrayAnomaly 由 systray_darwin.go 通过 cgo //export 提供：把本地加固逻辑
// 发现的异常上报给 Go 侧（tray 会写进 app.log），便于排查线上菜单结构错乱。
extern void systrayAnomaly(const char* msg);

NSMenuItem *find_menu_item(NSMenu *ourMenu, NSNumber *menuId);

static void report_anomaly(NSString* fmt, ...) {
  va_list args;
  va_start(args, fmt);
  NSString* msg = [[NSString alloc] initWithFormat:fmt arguments:args];
  va_end(args);
  systrayAnomaly(msg.UTF8String);
}

// count_tag_occurrences 统计整棵菜单树里 tag 相同的菜单项个数。
// 正常情况下每个 id 只出现一次；出现两次即为结构错乱（线上曾出现过
// 「组标题重复出现在自己的子菜单里」）。
static int count_tag_occurrences(NSMenu* menu, NSInteger tag) {
  int n = 0;
  for (NSMenuItem* it in menu.itemArray) {
    if (it.tag == tag) n++;
    if (it.hasSubmenu) n += count_tag_occurrences(it.submenu, tag);
  }
  return n;
}

- (void)add_or_update_menu_item:(MenuItem *)item {
  int menuId = [item->menuId intValue];
  int parentMenuId = [item->parentMenuId intValue];

  NSMenu *theMenu = self->menu;
  if (parentMenuId > 0) {
    NSMenuItem *parentItem = find_menu_item(menu, [NSNumber numberWithInt:parentMenuId]);
    if (parentItem == NULL) {
      // 找不到父节点时绝不能把条目挂到根菜单或孤儿菜单上：
      // 跳过并上报，等父节点就位后的下一次更新自动补齐。
      report_anomaly(@"systray: parent %d not found, skip item %d (%@)",
                     parentMenuId, menuId, item->title);
      return;
    }
    if (!parentItem.hasSubmenu) {
      NSMenu *sub = [[NSMenu alloc] init];
      [sub setAutoenablesItems:NO];
      [sub setDelegate:self];
      [parentItem setSubmenu:sub];
    }
    // 一律以父节点当前实际持有的子菜单为准。
    theMenu = parentItem.submenu;
  }

  // 只在本层菜单里找目标条目：一个条目只属于它的父菜单，
  // 递归查找反而可能在结构错乱时匹配到错误层级。
  NSMenuItem *menuItem = [theMenu itemWithTag:menuId];
  if (menuItem == NULL) {
    menuItem = [theMenu addItemWithTitle:item->title
                                 action:@selector(menuHandler:)
                          keyEquivalent:@""];
    [menuItem setRepresentedObject:item->menuId];
    report_anomaly(@"systray: add item %d parent %d (%@)",
                   menuId, parentMenuId, item->title);
  }
  [menuItem setTitle:item->title];
  [menuItem setTag:[item->menuId integerValue]];
  [menuItem setTarget:self];
  [menuItem setToolTip:item->tooltip];
  if (item->disabled == 1) {
    menuItem.enabled = FALSE;
  } else {
    menuItem.enabled = TRUE;
  }
  if (item->checked == 1) {
    menuItem.state = NSControlStateValueOn;
  } else {
    menuItem.state = NSControlStateValueOff;
  }
  // 结构自检：同一个 id 在整棵树里只能出现一次，多了立刻上报。
  if (count_tag_occurrences(self->menu, menuId) > 1) {
    report_anomaly(@"systray: duplicate item %d (%@) detected in menu tree",
                   menuId, item->title);
  }
}

// 鼠标（或键盘）在菜单里高亮某项时回调。representedObject 为 nil 时（分隔线、
// 子菜单标题等）按 0 上报，等价于「没有高亮项」。
- (void)menu:(NSMenu *)menu willHighlightItem:(NSMenuItem *)item
{
  int menuId = 0;
  if (item != nil) {
    NSNumber *n = [item representedObject];
    if (n != nil) {
      menuId = [n intValue];
    }
  }
  systray_menu_item_hovered(menuId);
}

- (void)menuDidClose:(NSMenu *)menu
{
  systray_menu_closed();
}

NSMenuItem *find_menu_item(NSMenu *ourMenu, NSNumber *menuId) {
  NSMenuItem *foundItem = [ourMenu itemWithTag:[menuId integerValue]];
  if (foundItem != NULL) {
    return foundItem;
  }
  NSArray *menu_items = ourMenu.itemArray;
  int i;
  for (i = 0; i < [menu_items count]; i++) {
    NSMenuItem *i_item = [menu_items objectAtIndex:i];
    if (i_item.hasSubmenu) {
      foundItem = find_menu_item(i_item.submenu, menuId);
      if (foundItem != NULL) {
        return foundItem;
      }
    }
  }

  return NULL;
};

- (void) add_separator:(NSNumber*) menuId
{
  [menu addItem: [NSMenuItem separatorItem]];
}

- (void) hide_menu_item:(NSNumber*) menuId
{
  NSMenuItem* menuItem = find_menu_item(menu, menuId);
  if (menuItem != NULL) {
    [menuItem setHidden:TRUE];
  }
}

- (void) setMenuItemIcon:(NSArray*)imageAndMenuId {
  NSImage* image = [imageAndMenuId objectAtIndex:0];
  NSNumber* menuId = [imageAndMenuId objectAtIndex:1];

  NSMenuItem* menuItem;
  menuItem = find_menu_item(menu, menuId);
  if (menuItem == NULL) {
    return;
  }
  menuItem.image = image;
}

- (void) show_menu_item:(NSNumber*) menuId
{
  NSMenuItem* menuItem = find_menu_item(menu, menuId);
  if (menuItem != NULL) {
    [menuItem setHidden:FALSE];
  }
}

- (void) quit
{
  [NSApp terminate:self];
}

@end

void registerSystray(void) {
  AppDelegate *delegate = [[AppDelegate alloc] init];
  [[NSApplication sharedApplication] setDelegate:delegate];
  // A workaround to avoid crashing on macOS versions before Catalina. Somehow
  // SIGSEGV would happen inside AppKit if [NSApp run] is called from a
  // different function, even if that function is called right after this.
  if (floor(NSAppKitVersionNumber) <= /*NSAppKitVersionNumber10_14*/ 1671){
    [NSApp run];
  }
}

int nativeLoop(void) {
  if (floor(NSAppKitVersionNumber) > /*NSAppKitVersionNumber10_14*/ 1671){
    [NSApp run];
  }
  return EXIT_SUCCESS;
}

void runInMainThread(SEL method, id object) {
  [(AppDelegate*)[NSApp delegate]
    performSelectorOnMainThread:method
                     withObject:object
                  waitUntilDone: YES];
}

void setIcon(const char* iconBytes, int length, bool template) {
  NSData* buffer = [NSData dataWithBytes: iconBytes length:length];
  NSImage *image = [[NSImage alloc] initWithData:buffer];
  [image setSize:NSMakeSize(16, 16)];
  image.template = template;
  runInMainThread(@selector(setIcon:), (id)image);
}

void setMenuItemIcon(const char* iconBytes, int length, int menuId, bool template) {
  NSData* buffer = [NSData dataWithBytes: iconBytes length:length];
  NSImage *image = [[NSImage alloc] initWithData:buffer];
  [image setSize:NSMakeSize(16, 16)];
  image.template = template;
  NSNumber *mId = [NSNumber numberWithInt:menuId];
  runInMainThread(@selector(setMenuItemIcon:), @[image, (id)mId]);
}

void setTitle(char* ctitle) {
  NSString* title = [[NSString alloc] initWithCString:ctitle
                                             encoding:NSUTF8StringEncoding];
  free(ctitle);
  runInMainThread(@selector(setTitle:), (id)title);
}

void setTooltip(char* ctooltip) {
  NSString* tooltip = [[NSString alloc] initWithCString:ctooltip
                                               encoding:NSUTF8StringEncoding];
  free(ctooltip);
  runInMainThread(@selector(setTooltip:), (id)tooltip);
}

void add_or_update_menu_item(int menuId, int parentMenuId, char* title, char* tooltip, short disabled, short checked, short isCheckable) {
  MenuItem* item = [[MenuItem alloc] initWithId: menuId withParentMenuId: parentMenuId withTitle: title withTooltip: tooltip withDisabled: disabled withChecked: checked];
  free(title);
  free(tooltip);
  runInMainThread(@selector(add_or_update_menu_item:), (id)item);
}

void add_separator(int menuId) {
  NSNumber *mId = [NSNumber numberWithInt:menuId];
  runInMainThread(@selector(add_separator:), (id)mId);
}

void hide_menu_item(int menuId) {
  NSNumber *mId = [NSNumber numberWithInt:menuId];
  runInMainThread(@selector(hide_menu_item:), (id)mId);
}

void show_menu_item(int menuId) {
  NSNumber *mId = [NSNumber numberWithInt:menuId];
  runInMainThread(@selector(show_menu_item:), (id)mId);
}

void quit() {
  runInMainThread(@selector(quit), nil);
}
