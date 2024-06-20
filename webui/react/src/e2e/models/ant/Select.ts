import { BaseComponent, NamedComponent } from 'e2e/models/BaseComponent';

/**
 * Represents the Select component from antd/es/select/index.js
 */
export class Select extends BaseComponent {
  readonly _menu = new BaseComponent({
    parent: this.root,
    selector: ':not(.ant-select-dropdown-hidden).ant-select-dropdown .rc-virtual-list-holder-inner',
  });

  readonly search = new BaseComponent({
    parent: this,
    selector: '.ant-select-selection-search input',
  });

  readonly selectionItem = new BaseComponent({
    parent: this,
    selector: '.ant-select-selection-item',
  });

  readonly #selectionOverflow = new BaseComponent({
    parent: this,
    selector: '.ant-select-selection-overflow',
  });

  readonly selectedOverflowItems = new selectionOverflowItem({
    parent: this.#selectionOverflow,
  });

  /**
   * Returns a selectedMenuOverflowItem in the Select component
   * @param {string} title - the title of the select item
   */
  selectedMenuOverflowItem(title: string): selectionOverflowItem {
    return new selectionOverflowItem({
      attachment: `[title="${title}"]`,
      parent: this.#selectionOverflow,
    });
  }

  /**
   * Shortcut to get all selected items.
   */
  async getSelectedMenuOverflowItemTitles(): Promise<string[]> {
    return await this.selectedOverflowItems.pwLocator.allTextContents();
  }

  /**
   * Shortcut open the menu for the select element.
   */
  async openMenu(): Promise<Select> {
    if (await this._menu.pwLocator.isVisible()) {
      try {
        await this._menu.pwLocator.press('Escape', { timeout: 500 });
        await this._menu.pwLocator.waitFor({ state: 'hidden' });
      } catch (e) {
        // it's fine if this fails, we are just ensuring they are all closed.
      }
    }
    await this.pwLocator.click();
    await this._menu.pwLocator.waitFor();
    await this.root._page.waitForTimeout(500); // ant/Popover - menus may reset input shortly after opening [ET-283]
    return this;
  }

  /**
   * Returns a menuItem in the Select component
   * @param {string} title - the title of the menu item
   */
  menuItem(title: string): BaseComponent {
    return new BaseComponent({
      parent: this._menu,
      selector: `div.ant-select-item[title="${title}"]`,
    });
  }

  /**
   * Selects a menu item with the specified title.
   * @param {string} title - title of the item to select
   */
  async selectMenuOption(title: string): Promise<void> {
    await this.openMenu();
    await this.menuItem(title).pwLocator.click();
    await this._menu.pwLocator.waitFor({ state: 'hidden' });
  }
}

/**
 * Represents a selectionOverflowItem from the Select component
 */
class selectionOverflowItem extends NamedComponent {
  defaultSelector = '.ant-select-selection-overflow-item ant-select-selection-item';

  readonly removeButton = new BaseComponent({
    parent: this,
    selector: '[aria-label="close"]',
  });
}